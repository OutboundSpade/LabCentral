package aws

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	lc "github.com/OutboundSpade/LabCentral/v2"
	"github.com/OutboundSpade/LabCentral/v2/build"
	"github.com/OutboundSpade/LabCentral/v2/internal/faultinject"
	"github.com/OutboundSpade/LabCentral/v2/internal/nativeproxy"
	aw "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	t "github.com/aws/aws-sdk-go-v2/service/ec2/types"
)

type buildGuard struct {
	p   *Provider
	api *ec2.Client
	c   lc.Command
	mu  sync.Mutex
}

func (p *Provider) BuildProxy(ctx context.Context, c lc.Command, cfg *build.Config) (func(), error) {
	options := []func(*config.LoadOptions) error{config.WithRegion(c.Environment.AWS.Region)}
	if c.Environment.AWS.Profile != "" {
		options = append(options, config.WithSharedConfigProfile(c.Environment.AWS.Profile))
	}
	sdk, err := config.LoadDefaultConfig(ctx, options...)
	if err != nil {
		return nil, err
	}
	api, _, err := client(ctx, c.Environment)
	if err != nil {
		return nil, err
	}
	guard := &buildGuard{p: p, api: api, c: c}
	upstream, _ := url.Parse("https://ec2." + c.Environment.AWS.Region + ".amazonaws.com")
	endpoint, close, err := nativeproxy.Start(faultinject.WithOperation(ctx, "aws", "image.build"), nativeproxy.Config{
		Upstream: upstream, Protocol: "aws", Transport: http.DefaultTransport,
		Step: func(_ *http.Request, body []byte) string {
			v, _ := url.ParseQuery(string(body))
			return "packer." + v.Get("Action")
		},
		Before: guard.before, After: guard.after, Do: guard.do, Verify: func(ctx context.Context, r *http.Request, b []byte) error {
			_, err := guard.before(ctx, r, b)
			return err
		},
		Prepare: func(r *http.Request, body []byte) error {
			credentials, err := sdk.Credentials.Retrieve(r.Context())
			if err != nil {
				return err
			}
			r.Header.Del("Authorization")
			r.Header.Del("X-Amz-Security-Token")
			hash := sha256.Sum256(body)
			return v4.NewSigner().SignHTTP(r.Context(), credentials, r, hex.EncodeToString(hash[:]), "ec2", c.Environment.AWS.Region, time.Now())
		},
	})
	if err != nil {
		return nil, err
	}
	var native map[string]any
	if err = json.Unmarshal(cfg.Builders[0], &native); err != nil {
		close()
		return nil, err
	}
	native["custom_endpoint_ec2"] = endpoint + "/"
	cfg.Builders[0], err = json.Marshal(native)
	if err != nil {
		close()
		return nil, err
	}
	return close, nil
}
func buildCreateTags(v url.Values, owner, role string, kinds ...string) error {
	for _, kind := range kinds {
		found := false
		for n := 1; n <= 8; n++ {
			prefix := fmt.Sprintf("TagSpecification.%d.", n)
			if v.Get(prefix+"ResourceType") != kind {
				continue
			}
			tags := map[string]string{}
			for index := 1; index <= 64; index++ {
				tag := fmt.Sprintf("%sTag.%d.", prefix, index)
				if key := v.Get(tag + "Key"); key != "" {
					tags[key] = v.Get(tag + "Value")
				}
			}
			found = tags["labcentral:owner"] == owner && tags["labcentral:version"] == "2" && tags["labcentral:role"] == role
		}
		if !found {
			return fmt.Errorf("ownership_mismatch: builder create lacks reserved %s ownership", kind)
		}
	}
	return nil
}
func queryIDs(v url.Values, key string) []string {
	ids := []string{}
	if value := v.Get(key); value != "" {
		ids = append(ids, value)
	}
	for n := 1; n <= 64; n++ {
		if value := v.Get(fmt.Sprintf("%s.%d", key, n)); value != "" {
			ids = append(ids, value)
		}
	}
	return ids
}
func (g *buildGuard) instance(ctx context.Context, id string, children bool) error {
	result, err := g.api.DescribeInstances(ctx, &ec2.DescribeInstancesInput{InstanceIds: []string{id}})
	if apiCode(err, "InvalidInstanceID.NotFound") {
		return nil
	}
	if err != nil {
		return err
	}
	count := 0
	for _, reservation := range result.Reservations {
		for _, i := range reservation.Instances {
			count++
			if aw.ToString(i.InstanceId) != id || !owned(i.Tags, g.c.Owner) {
				return fmt.Errorf("ownership_mismatch: builder instance retained")
			}
			if children && i.State != nil && i.State.Name != "terminated" {
				if err = verifyBuildInstanceChildren(ctx, g, i); err != nil {
					return err
				}
			}
		}
	}
	if count != 1 {
		return fmt.Errorf("recovery_required: builder instance identity unconfirmed")
	}
	return nil
}
func (g *buildGuard) object(ctx context.Context, id string) error {
	switch {
	case strings.HasPrefix(id, "i-"):
		return g.instance(ctx, id, false)
	case strings.HasPrefix(id, "ami-"):
		r, e := g.api.DescribeImages(ctx, &ec2.DescribeImagesInput{ImageIds: []string{id}})
		if apiCode(e, "InvalidAMIID.NotFound") {
			return nil
		}
		if e != nil {
			return e
		}
		if len(r.Images) == 0 {
			return nil
		}
		if len(r.Images) == 1 && owned(r.Images[0].Tags, g.c.Owner) {
			return nil
		}
	case strings.HasPrefix(id, "vol-"):
		r, e := g.api.DescribeVolumes(ctx, &ec2.DescribeVolumesInput{VolumeIds: []string{id}})
		if apiCode(e, "InvalidVolume.NotFound") {
			return nil
		}
		if e != nil {
			return e
		}
		if len(r.Volumes) == 1 && owned(r.Volumes[0].Tags, g.c.Owner) {
			return nil
		}
	case strings.HasPrefix(id, "snap-"):
		r, e := g.api.DescribeSnapshots(ctx, &ec2.DescribeSnapshotsInput{SnapshotIds: []string{id}})
		if apiCode(e, "InvalidSnapshot.NotFound") {
			return nil
		}
		if e != nil {
			return e
		}
		if len(r.Snapshots) == 1 && owned(r.Snapshots[0].Tags, g.c.Owner) {
			return nil
		}
	case strings.HasPrefix(id, "eni-"):
		r, e := g.api.DescribeNetworkInterfaces(ctx, &ec2.DescribeNetworkInterfacesInput{NetworkInterfaceIds: []string{id}})
		if apiCode(e, "InvalidNetworkInterfaceID.NotFound") {
			return nil
		}
		if e != nil {
			return e
		}
		if len(r.NetworkInterfaces) == 1 && owned(r.NetworkInterfaces[0].TagSet, g.c.Owner) {
			return nil
		}
	}
	return fmt.Errorf("ownership_mismatch: builder resource retained")
}
func (g *buildGuard) before(ctx context.Context, _ *http.Request, body []byte) ([]byte, error) {
	v, err := url.ParseQuery(string(body))
	if err != nil {
		return nil, err
	}
	action := v.Get("Action")
	if strings.HasPrefix(action, "Describe") || strings.HasPrefix(action, "Get") {
		return body, nil
	}
	switch action {
	case "RunInstances":
		if v.Get("ImageId") != g.c.Image.Base.ID {
			return nil, fmt.Errorf("builder base differs from pinned source")
		}
		err = buildCreateTags(v, g.c.Owner, g.c.Image.Name, "instance", "volume", "network-interface")
	case "CreateImage":
		if v.Get("Name") != "labcentral-"+g.c.Owner {
			return nil, fmt.Errorf("builder image name differs from reserved identity")
		}
		err = buildCreateTags(v, g.c.Owner, g.c.Image.Name, "image", "snapshot")
		if err == nil {
			err = g.instance(ctx, v.Get("InstanceId"), true)
		}
	case "StopInstances", "TerminateInstances", "ModifyInstanceAttribute":
		ids := queryIDs(v, "InstanceId")
		if len(ids) == 0 {
			return nil, fmt.Errorf("missing builder instance identity")
		}
		for _, id := range ids {
			if err = g.instance(ctx, id, action != "ModifyInstanceAttribute"); err != nil {
				break
			}
		}
	case "CreateTags", "DeleteTags":
		for n := 1; n <= 64; n++ {
			key := v.Get(fmt.Sprintf("Tag.%d.Key", n))
			value := v.Get(fmt.Sprintf("Tag.%d.Value", n))
			if strings.HasPrefix(key, "labcentral:") && (action == "DeleteTags" || (key == "labcentral:owner" && value != g.c.Owner) || (key == "labcentral:version" && value != "2")) {
				return nil, fmt.Errorf("builder cannot replace ownership markings")
			}
		}
		ids := queryIDs(v, "ResourceId")
		if len(ids) == 0 {
			return nil, fmt.Errorf("missing builder tag target")
		}
		for _, id := range ids {
			if err = g.object(ctx, id); err != nil {
				break
			}
		}
	case "DeregisterImage", "ModifyImageAttribute":
		if v.Get("DeleteAssociatedSnapshots") == "true" {
			return nil, fmt.Errorf("builder must delete individually verified snapshots")
		}
		err = g.object(ctx, v.Get("ImageId"))
	case "DeleteSnapshot":
		err = g.object(ctx, v.Get("SnapshotId"))
	case "DeleteVolume":
		err = g.object(ctx, v.Get("VolumeId"))
	case "DeleteNetworkInterface":
		err = g.object(ctx, v.Get("NetworkInterfaceId"))
	default:
		return nil, fmt.Errorf("builder native mutation %s is not supported", action)
	}
	return body, err
}

type buildWireTag struct {
	Key   string `xml:"key"`
	Value string `xml:"value"`
}
type buildWireInstance struct {
	ID   string         `xml:"instanceId"`
	Tags []buildWireTag `xml:"tagSet>item"`
	NICs []struct {
		ID string `xml:"networkInterfaceId"`
	} `xml:"networkInterfaceSet>item"`
	Disks []struct {
		ID string `xml:"ebs>volumeId"`
	} `xml:"blockDeviceMapping>item"`
}

func wireOwned(tags []buildWireTag, owner string) bool {
	got, version := "", ""
	for _, tag := range tags {
		if tag.Key == "labcentral:owner" {
			got = tag.Value
		}
		if tag.Key == "labcentral:version" {
			version = tag.Value
		}
	}
	return got == owner && version == "2"
}
func (p *Provider) buildResources(c lc.Command) ([]lc.Resource, error) {
	var resources []lc.Resource
	err := p.Journal.Load(c.Owner+"/build-resources", &resources)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	for _, r := range resources {
		if r.Owner != c.Owner {
			return nil, fmt.Errorf("ownership_mismatch: native build receipt")
		}
	}
	return resources, nil
}
func mergeBuildResource(resources []lc.Resource, r lc.Resource) []lc.Resource {
	for index, old := range resources {
		if old.Kind == r.Kind && old.ID == r.ID {
			known := map[lc.NativeResource]bool{}
			for _, child := range old.Children {
				known[child] = true
			}
			for _, child := range r.Children {
				if !known[child] {
					old.Children = append(old.Children, child)
				}
			}
			resources[index] = old
			return resources
		}
	}
	return append(resources, r)
}
func (g *buildGuard) after(ctx context.Context, _ *http.Request, body []byte, reply nativeproxy.Reply) error {
	values, _ := url.ParseQuery(string(body))
	action := values.Get("Action")
	if action != "RunInstances" && action != "DescribeInstances" && action != "CreateImage" && action != "DescribeImages" {
		return nil
	}
	var native struct {
		Instances    []buildWireInstance `xml:"instancesSet>item"`
		Reservations []struct {
			Instances []buildWireInstance `xml:"instancesSet>item"`
		} `xml:"reservationSet>item"`
		ImageID string `xml:"imageId"`
		Images  []struct {
			ID    string         `xml:"imageId"`
			Tags  []buildWireTag `xml:"tagSet>item"`
			Disks []struct {
				ID string `xml:"ebs>snapshotId"`
			} `xml:"blockDeviceMapping>item"`
		} `xml:"imagesSet>item"`
	}
	if err := xml.Unmarshal(reply.Body, &native); err != nil {
		return err
	}
	var found []lc.Resource
	for _, reservation := range native.Reservations {
		native.Instances = append(native.Instances, reservation.Instances...)
	}
	for _, i := range native.Instances {
		if action != "RunInstances" && !wireOwned(i.Tags, g.c.Owner) {
			continue
		}
		if i.ID == "" {
			continue
		}
		data, _ := json.Marshal(instancePolicy{TaggedENIs: true})
		r := lc.Resource{Kind: "machine", Name: g.c.Image.Name, ID: i.ID, Owner: g.c.Owner, Data: data}
		for _, nic := range i.NICs {
			if nic.ID != "" {
				r.Children = append(r.Children, lc.NativeResource{Kind: "eni", ID: nic.ID})
			}
		}
		for _, disk := range i.Disks {
			if disk.ID != "" {
				r.Children = append(r.Children, lc.NativeResource{Kind: "volume", ID: disk.ID})
			}
		}
		found = append(found, r)
	}
	if action == "CreateImage" && native.ImageID != "" {
		found = append(found, lc.Resource{Kind: "image", Name: g.c.Image.Name, ID: native.ImageID, Owner: g.c.Owner})
	}
	for _, i := range native.Images {
		if wireOwned(i.Tags, g.c.Owner) {
			r := lc.Resource{Kind: "image", Name: g.c.Image.Name, ID: i.ID, Owner: g.c.Owner}
			for _, disk := range i.Disks {
				if disk.ID != "" {
					r.Children = append(r.Children, lc.NativeResource{Kind: "snapshot", ID: disk.ID})
				}
			}
			found = append(found, r)
		}
	}
	if len(found) == 0 {
		return nil
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	resources, err := g.p.buildResources(g.c)
	if err != nil {
		return err
	}
	for _, r := range found {
		resources = mergeBuildResource(resources, r)
	}
	return g.p.Journal.Save(g.c.Owner+"/build-resources", resources)
}

func verifyBuildInstanceChildren(ctx context.Context, g *buildGuard, i t.Instance) error {
	c := g.c
	resources, err := g.p.buildResources(c)
	if err != nil {
		return err
	}
	for _, r := range resources {
		if r.Kind == "machine" && r.ID == aw.ToString(i.InstanceId) {
			c.Prior = r
			break
		}
	}
	return verifyInstanceChildren(ctx, g.api, c, i, true)
}
