package aws

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"

	lc "github.com/OutboundSpade/LabCentral/v2"
	"github.com/OutboundSpade/LabCentral/v2/internal/faultinject"
	"github.com/OutboundSpade/LabCentral/v2/internal/journal"
	"github.com/OutboundSpade/LabCentral/v2/internal/nativeproxy"
	aw "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	t "github.com/aws/aws-sdk-go-v2/service/ec2/types"
)

type buildMutation struct{ Key, Action, Token, Name string }
type buildMutationResult struct {
	Resource lc.Resource
	Reply    nativeproxy.Reply
}
type buildNativeReplyError struct{ Reply nativeproxy.Reply }

func (e *buildNativeReplyError) Error() string {
	return "builder native request returned an error response"
}

func (g *buildGuard) mutations() ([]buildMutation, error) {
	var requests []buildMutation
	err := g.p.Journal.Load(g.c.Owner+"/build-mutations", &requests)
	if os.IsNotExist(err) {
		return nil, nil
	}
	return requests, err
}
func (g *buildGuard) registerMutation(body []byte) (buildMutation, bool, error) {
	v, err := url.ParseQuery(string(body))
	if err != nil {
		return buildMutation{}, false, err
	}
	action := v.Get("Action")
	if action != "RunInstances" && action != "CreateImage" {
		return buildMutation{}, false, nil
	}
	sum := sha256.Sum256(body)
	request := buildMutation{Key: g.c.Owner + "/native/" + hex.EncodeToString(sum[:]), Action: action, Token: v.Get("ClientToken"), Name: v.Get("Name")}
	if action == "RunInstances" && request.Token == "" {
		return request, true, fmt.Errorf("builder instance requires a native idempotency token")
	}
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
	requests = append(requests, request)
	return request, true, g.p.Journal.Save(g.c.Owner+"/build-mutations", requests)
}
func rejectedBuildReply(action string, reply nativeproxy.Reply) bool {
	var result struct {
		Errors []struct {
			Code string `xml:"Code"`
		} `xml:"Errors>Error"`
	}
	if xml.Unmarshal(reply.Body, &result) != nil {
		return false
	}
	for _, e := range result.Errors {
		if definiteEC2Rejection(action, e.Code) {
			return true
		}
	}
	return false
}
func (g *buildGuard) discoverMutation(ctx context.Context, request buildMutation) (buildMutationResult, bool, error) {
	var resource lc.Resource
	switch request.Action {
	case "RunInstances":
		out, err := g.api.DescribeInstances(ctx, &ec2.DescribeInstancesInput{Filters: []t.Filter{{Name: aw.String("client-token"), Values: []string{request.Token}}}})
		if err != nil {
			return buildMutationResult{}, false, err
		}
		var instances []t.Instance
		for _, reservation := range out.Reservations {
			instances = append(instances, reservation.Instances...)
		}
		if len(instances) == 0 {
			return buildMutationResult{}, false, nil
		}
		if len(instances) != 1 {
			return buildMutationResult{}, false, fmt.Errorf("recovery_required: ambiguous build client token")
		}
		i := instances[0]
		if !owned(i.Tags, g.c.Owner) {
			return buildMutationResult{}, false, fmt.Errorf("ownership_mismatch: unknown build instance retained")
		}
		resource = lc.Resource{Kind: "machine", Name: g.c.Image.Name, ID: aw.ToString(i.InstanceId), Owner: g.c.Owner}
		for _, nic := range i.NetworkInterfaces {
			resource.Children = append(resource.Children, lc.NativeResource{Kind: "eni", ID: aw.ToString(nic.NetworkInterfaceId)})
		}
		for _, disk := range i.BlockDeviceMappings {
			if disk.Ebs != nil {
				resource.Children = append(resource.Children, lc.NativeResource{Kind: "volume", ID: aw.ToString(disk.Ebs.VolumeId)})
			}
		}
	case "CreateImage":
		out, err := g.api.DescribeImages(ctx, &ec2.DescribeImagesInput{Owners: []string{"self"}, Filters: []t.Filter{{Name: aw.String("name"), Values: []string{request.Name}}}})
		if err != nil {
			return buildMutationResult{}, false, err
		}
		if len(out.Images) == 0 {
			return buildMutationResult{}, false, nil
		}
		if len(out.Images) != 1 {
			return buildMutationResult{}, false, fmt.Errorf("recovery_required: ambiguous captured image")
		}
		image := out.Images[0]
		if !owned(image.Tags, g.c.Owner) {
			return buildMutationResult{}, false, fmt.Errorf("ownership_mismatch: unknown captured image retained")
		}
		resource = lc.Resource{Kind: "image", Name: g.c.Image.Name, ID: aw.ToString(image.ImageId), Owner: g.c.Owner}
		for _, disk := range image.BlockDeviceMappings {
			if disk.Ebs != nil {
				resource.Children = append(resource.Children, lc.NativeResource{Kind: "snapshot", ID: aw.ToString(disk.Ebs.SnapshotId)})
			}
		}
	default:
		return buildMutationResult{}, false, fmt.Errorf("unsupported recorded build mutation")
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	resources, err := g.p.buildResources(g.c)
	if err != nil {
		return buildMutationResult{}, false, err
	}
	resources = mergeBuildResource(resources, resource)
	if err = g.p.Journal.Save(g.c.Owner+"/build-resources", resources); err != nil {
		return buildMutationResult{}, false, err
	}
	return buildMutationResult{Resource: resource}, true, nil
}
func (g *buildGuard) do(ctx context.Context, r *http.Request, body []byte, perform func(context.Context) (nativeproxy.Reply, error)) (nativeproxy.Reply, error) {
	request, create, err := g.registerMutation(body)
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
			if status := faultinject.Status(err); status == 400 || status == 403 || status == 429 {
				return buildMutationResult{}, journal.Rejected(err)
			}
			return buildMutationResult{}, err
		}
		if reply.Status >= 400 {
			failure := &buildNativeReplyError{reply}
			if rejectedBuildReply(request.Action, reply) {
				return buildMutationResult{}, journal.Rejected(failure)
			}
			return buildMutationResult{}, failure
		}
		return buildMutationResult{Reply: reply}, nil
	}, func() (buildMutationResult, bool, error) { return g.discoverMutation(ctx, request) })
	var response *buildNativeReplyError
	if errors.As(err, &response) {
		return response.Reply, nil
	}
	if err != nil {
		return nativeproxy.Reply{}, err
	}
	if result.Reply.Status != 0 {
		return result.Reply, nil
	}
	// Discovery recovered the effect, not Packer's transient execution state.
	// Stop the builder rather than inventing a native response or rerunning author
	// code; the CLI will qualify the capture or clean the incomplete attempt.
	return nativeproxy.Reply{Status: 409, Header: http.Header{"Content-Type": []string{"text/xml"}}, Body: []byte(`<Response><Errors><Error><Code>RecoveryRequired</Code><Message>native effect recovered; use labcentral recover</Message></Error></Errors></Response>`)}, nil
}
func (p *Provider) reconcileBuildMutations(ctx context.Context, a *ec2.Client, c lc.Command) error {
	guard := &buildGuard{p: p, api: a, c: c}
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
			continue
		}
		_, err = journal.Do(ctx, p.Journal, request.Key, func() (buildMutationResult, error) {
			return buildMutationResult{}, fmt.Errorf("cannot replay an interrupted build mutation")
		}, func() (buildMutationResult, bool, error) { return guard.discoverMutation(ctx, request) })
		if err != nil {
			return fmt.Errorf("recovery_required: %s build outcome unresolved; allocation and cleanup intent retained: %w", request.Action, err)
		}
	}
	return nil
}
