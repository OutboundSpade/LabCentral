package proxmox

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"

	lc "github.com/OutboundSpade/LabCentral/v2"
	"github.com/OutboundSpade/LabCentral/v2/internal/faultinject"
	"github.com/OutboundSpade/LabCentral/v2/internal/journal"
	"github.com/OutboundSpade/LabCentral/v2/internal/nativeproxy"
)

type buildMutation struct{ Key, Action string }
type buildMutationResult struct {
	Resource lc.Resource
	Reply    nativeproxy.Reply
}
type buildNativeReplyError struct{ Reply nativeproxy.Reply }

func (e *buildNativeReplyError) Error() string {
	return "builder native request returned an error response"
}

type proxmoxBuildGuard struct {
	p   *Provider
	api *Client
	c   lc.Command
	mu  sync.Mutex
}

func (g *proxmoxBuildGuard) mutations() ([]buildMutation, error) {
	var requests []buildMutation
	err := g.p.Journal.Load(g.c.Owner+"/build-mutations", &requests)
	if os.IsNotExist(err) {
		return nil, nil
	}
	return requests, err
}
func (g *proxmoxBuildGuard) register(r *http.Request, body []byte) (buildMutation, bool, error) {
	action := ""
	if r.Method == "POST" {
		if strings.HasSuffix(r.URL.Path, "/clone") {
			action = "clone"
		}
		if strings.HasSuffix(r.URL.Path, "/template") {
			action = "template"
		}
	}
	if action == "" {
		return buildMutation{}, false, nil
	}
	sum := sha256.Sum256(append([]byte(r.Method+" "+r.URL.Path+"\n"), body...))
	request := buildMutation{Key: g.c.Owner + "/native/" + hex.EncodeToString(sum[:]), Action: action}
	g.mu.Lock()
	defer g.mu.Unlock()
	requests, err := g.mutations()
	if err != nil {
		return request, true, err
	}
	for _, old := range requests {
		if old.Key == request.Key {
			return old, true, nil
		}
	}
	return request, true, g.p.Journal.Save(g.c.Owner+"/build-mutations", append(requests, request))
}
func (g *proxmoxBuildGuard) discover(ctx context.Context, request buildMutation) (buildMutationResult, bool, error) {
	v, err := g.api.Config(ctx, g.c.Machine.VMID)
	if err != nil {
		var response *HTTPError
		if errors.As(err, &response) && (response.Status == 404 || response.Status == 500) {
			return buildMutationResult{}, false, nil
		}
		return buildMutationResult{}, false, err
	}
	if err = checkOwner(v, g.c.Owner, g.c.Image.Name); err != nil {
		return buildMutationResult{}, false, err
	}
	if v.Lock != "" {
		return buildMutationResult{}, false, fmt.Errorf("recovery_required: native build task still locked; retain allocation")
	}
	if request.Action == "template" && v.Template != 1 {
		return buildMutationResult{}, false, nil
	}
	kind := "machine"
	if v.Template == 1 {
		kind = "image"
	}
	return buildMutationResult{Resource: lc.Resource{Kind: kind, Name: g.c.Image.Name, ID: fmt.Sprint(g.c.Machine.VMID), Owner: g.c.Owner}}, true, nil
}
func (g *proxmoxBuildGuard) do(ctx context.Context, r *http.Request, body []byte, perform func(context.Context) (nativeproxy.Reply, error)) (nativeproxy.Reply, error) {
	request, create, err := g.register(r, body)
	if err != nil {
		return nativeproxy.Reply{}, err
	}
	if !create {
		return perform(ctx)
	}
	result, err := journal.Do(ctx, g.p.Journal, request.Key, func() (buildMutationResult, error) {
		reply, err := perform(ctx)
		if err != nil {
			if nativeproxy.IsBeforeSubmission(err) {
				return buildMutationResult{}, journal.Rejected(err)
			}
			if status := faultinject.Status(err); status == 400 || status == 401 || status == 403 || status == 429 {
				return buildMutationResult{}, journal.Rejected(err)
			}
			return buildMutationResult{}, err
		}
		if reply.Status >= 400 {
			failure := &buildNativeReplyError{reply}
			if reply.Status == 400 || reply.Status == 401 || reply.Status == 403 || reply.Status == 429 {
				return buildMutationResult{}, journal.Rejected(failure)
			}
			return buildMutationResult{}, failure
		}
		return buildMutationResult{Reply: reply}, nil
	}, func() (buildMutationResult, bool, error) { return g.discover(ctx, request) })
	var reply *buildNativeReplyError
	if errors.As(err, &reply) {
		return reply.Reply, nil
	}
	if err != nil {
		return nativeproxy.Reply{}, err
	}
	if result.Reply.Status != 0 {
		return result.Reply, nil
	}
	return nativeproxy.Reply{Status: 409, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: []byte(`{"data":null,"message":"native effect recovered; use labcentral recover"}`)}, nil
}
func (p *Provider) reconcileBuildMutations(ctx context.Context, api *Client, c lc.Command) error {
	guard := &proxmoxBuildGuard{p: p, api: api, c: c}
	requests, err := guard.mutations()
	if err != nil {
		return err
	}
	for _, request := range requests {
		known, err := p.Journal.HasIntent(request.Key)
		if err != nil {
			return err
		}
		if !known {
			continue
		}
		var result buildMutationResult
		if err = p.Journal.Load(request.Key, &result); err == nil {
			if err = buildTaskSettled(ctx, api, result.Reply); err != nil {
				return err
			}
			continue
		}
		_, err = journal.Do(ctx, p.Journal, request.Key, func() (buildMutationResult, error) {
			return buildMutationResult{}, fmt.Errorf("cannot replay interrupted native build mutation")
		}, func() (buildMutationResult, bool, error) { return guard.discover(ctx, request) })
		if err != nil {
			return fmt.Errorf("recovery_required: %s build outcome unresolved; allocation and cleanup intent retained: %w", request.Action, err)
		}
	}
	return nil
}

func buildTaskSettled(ctx context.Context, c *Client, reply nativeproxy.Reply) error {
	if reply.Status == 0 {
		return nil
	}
	var envelope struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(reply.Body, &envelope); err != nil {
		return err
	}
	var task string
	if string(envelope.Data) == "null" || len(envelope.Data) == 0 {
		return nil
	}
	if err := json.Unmarshal(envelope.Data, &task); err != nil {
		return err
	}
	if !strings.HasPrefix(task, "UPID:"+c.Node+":") {
		return fmt.Errorf("recovery_required: invalid native build task identity")
	}
	var status struct {
		Status string `json:"status"`
	}
	if err := c.Call(ctx, "GET", "/nodes/"+url.PathEscape(c.Node)+"/tasks/"+url.PathEscape(task)+"/status", nil, &status); err != nil {
		return err
	}
	if status.Status != "stopped" {
		return fmt.Errorf("recovery_required: native build task still running; retain allocation and retry recovery")
	}
	return nil
}
