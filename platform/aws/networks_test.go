package aws

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	lc "github.com/OutboundSpade/LabCentral/v2"
	"github.com/OutboundSpade/LabCentral/v2/internal/journal"
	"github.com/OutboundSpade/LabCentral/v2/internal/sshtest"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"io"
	"net/http"
	"strings"
	"testing"
)

func twoNetworks() lc.Command {
	c := networkCommand(true)
	c.Environment.AWS.Types = []lc.InstanceType{{Name: "t3.large", CPU: 2, MemoryMiB: 8192, MaxNICs: 3}}
	c.Source.Lab.Networks = map[string]lc.Network{"a": {Egress: "internet"}, "b": {}}
	c.Source.Lab.Machines = map[string]lc.Machine{
		"a-client": {CPU: 2, Memory: "2Gi", NICs: []lc.NIC{{Network: "a"}}},
		"b-client": {CPU: 2, Memory: "2Gi", NICs: []lc.NIC{{Network: "b"}}},
		"router":   {CPU: 2, Memory: "2Gi", Forwarding: true, NICs: []lc.NIC{{Network: "b"}, {Network: "a"}}},
	}
	return c
}
func TestMultiNetworkRequestsAndInterruptedChildren(t *testing.T) {
	for _, loss := range []string{"", "CreateSubnet:segment-b-subnet", "CreateRouteTable:segment-b-route", "CreateSecurityGroup:segment-b-sg", "CreateSecurityGroup:management-sg"} {
		t.Run(loss, func(t *testing.T) {
			f, a := newNetworkEC2(t)
			f.loss = loss
			p := &Provider{Journal: journal.Journal{Root: t.TempDir()}}
			c := twoNetworks()
			result, err := p.network(context.Background(), a, c)
			if loss != "" {
				if err == nil {
					t.Fatal("response loss accepted")
				}
				if _, err = p.findNetwork(context.Background(), a, c); err != nil {
					t.Fatal("uncertain child could not be discovered", err)
				}
				result, err = p.network(context.Background(), a, c)
			}
			if err != nil {
				t.Fatal(err)
			}
			var n networkState
			if err = json.Unmarshal(result.Resource.Data, &n); err != nil {
				t.Fatal(err)
			}
			if len(n.Segments) != 2 || n.ManagementSG == "" {
				t.Fatal("network layout incomplete", n)
			}
			for _, seg := range n.Segments {
				if seg.CIDR == "" || seg.Subnet == "" || seg.SG == "" || seg.Route == "" {
					t.Fatal("missing per network resource", seg)
				}
			}
			subnets := map[string]bool{}
			routeTables, groups := 0, 0
			for _, v := range f.requests {
				switch v.Get("Action") {
				case "CreateSubnet":
					subnets[v.Get("CidrBlock")] = true
					requestValue(t, v, "AvailabilityZone", "us-east-1a")
				case "CreateRouteTable":
					routeTables++
				case "CreateSecurityGroup":
					groups++
				case "CreateVpcEndpoint":
					requestValue(t, v, "RouteTableId.1", n.Segments[0].Route)
					requestValue(t, v, "RouteTableId.2", n.Segments[1].Route)
				case "AuthorizeSecurityGroupIngress":
					if v.Get("IpPermissions.1.FromPort") == "22" || v.Get("IpPermissions.1.FromPort") == "8443" {
						if v.Get("IpPermissions.1.Groups.1.GroupId") == n.GatewaySG && v.Get("GroupId") != n.ManagementSG {
							t.Fatal("management admitted on secondary NIC group", v)
						}
					}
				case "AuthorizeSecurityGroupEgress":
					if v.Get("GroupId") == n.Segments[1].SG && v.Get("IpPermissions.1.IpRanges.1.CidrIp") == "0.0.0.0/0" {
						t.Fatal("isolated network got Internet egress")
					}
				}
			}
			if len(subnets) != 3 || !subnets["10.77.0.0/24"] || !subnets["10.77.1.0/24"] || !subnets["10.77.255.0/28"] || routeTables != 3 || groups != 4 {
				t.Fatal("duplicate/missing layout", subnets, routeTables, groups)
			}
			c.Prior = result.Resource
			f.deleteLoss = "DeleteSubnet"
			if _, err = p.destroyNetwork(context.Background(), a, c); err == nil {
				t.Fatal("uncertain cleanup accepted")
			}
			r, err := p.destroyNetwork(context.Background(), a, c)
			if err != nil || !r.Absent || len(f.objects) != 0 {
				t.Fatal("recovered cleanup leaked", err, f.objects)
			}
		})
	}
}
func TestMultiENILaunchRecoveryAndRouterRoutes(t *testing.T) {
	for _, reject := range []string{"", "InvalidParameterValue"} {
		t.Run(reject, func(t *testing.T) {
			f, a := newNetworkEC2(t)
			f.routeReject = reject
			p := &Provider{Journal: journal.Journal{Root: t.TempDir()}}
			c := twoNetworks()
			network, err := p.network(context.Background(), a, c)
			if err != nil {
				t.Fatal(err)
			}
			c.Network = network.Resource
			private, pub := sshtest.Key(t)
			sshServer := sshtest.New(t, nil, nil)
			gateway := sshtest.New(t, func(string) string { return sshServer.Address }, nil)
			topology := lc.Plan{Source: c.Source, Environment: c.Environment}
			for _, name := range lc.SortedMachineNames(c.Source.Lab) {
				topology.Machines = append(topology.Machines, lc.PlannedMachine{Name: name, Spec: c.Source.Lab.Machines[name]})
			}
			if err = lc.PlanNetworks(&topology, nil); err != nil {
				t.Fatal(err)
			}
			c.Machine = topology.Machines[2]
			c.Machine.NativeImage = "ami-base"
			c.Machine.InstanceType = "t3.large"
			c.Machine.DiskGiB = 20
			c.Machine.PublicKey = pub
			c.Machine.HostPrivateKey = string(private)
			c.Machine.HostPublicKey = sshServer.HostKey
			c.ClientPrivateKey = []byte(private)
			c.Operation = "owner/machine/router"
			data, _ := json.Marshal(lc.GatewayIdentity{HostPublicKey: gateway.HostKey, ENI: "eni-gateway"})
			c.Gateway = lc.Resource{Kind: "gateway", Owner: "owner", ID: "i-gateway", Address: gateway.Address, Complete: true, Data: data}
			f.loss = "RunInstances:machine:router"
			if _, err = p.machine(context.Background(), a, c); err == nil {
				t.Fatal("lost multi-ENI create accepted")
			}
			c.Recovery = true
			r, err := p.machine(context.Background(), a, c)
			if err != nil || !r.Resource.Complete {
				t.Fatal("recovered launch failed", err)
			}
			var ns networkState
			_ = json.Unmarshal(c.Network.Data, &ns)
			launches, modified, routes := 0, 0, 0
			for _, v := range f.requests {
				switch v.Get("Action") {
				case "RunInstances":
					launches++
					requestValue(t, v, "TagSpecification.3.ResourceType", "network-interface")
					for j, nic := range c.Machine.NICs {
						prefix := fmt.Sprintf("NetworkInterface.%d.", j+1)
						requestValue(t, v, prefix+"DeviceIndex", fmt.Sprint(j))
						requestValue(t, v, prefix+"PrivateIpAddress", nic.Address)
						requestValue(t, v, prefix+"AssociatePublicIpAddress", "false")
						requestValue(t, v, prefix+"DeleteOnTermination", "true")
						if j == 0 {
							requestValue(t, v, prefix+"SecurityGroupId.2", ns.ManagementSG)
						} else if v.Get(prefix+"SecurityGroupId.2") != "" {
							t.Fatal("secondary NIC got management", v)
						}
					}
					raw, err := base64.StdEncoding.DecodeString(v.Get("UserData"))
					if err != nil || !strings.Contains(string(raw), "device-number") || !strings.Contains(string(raw), "10.77.1.12/24") || !strings.Contains(string(raw), "net.ipv4.ip_forward=1") {
						t.Fatal("non-explicit guest network", err, string(raw))
					}
				case "ModifyNetworkInterfaceAttribute":
					modified++
					requestValue(t, v, "SourceDestCheck.Value", "false")
				case "CreateRoute":
					if v.Get("NetworkInterfaceId") != "" {
						routes++
						if v.Get("DestinationCidrBlock") == "0.0.0.0/0" {
							t.Fatal("router installed a default route")
						}
					}
				}
			}
			if launches != 1 || modified != 2 || routes != 2 {
				t.Fatal("recovery replaced or missed ENIs/routes", launches, modified, routes)
			}
			if reject == "" && len(f.routes) != 2 {
				t.Fatal("remote subnet routes not installed", f.routes)
			}
			// Inline ENIs follow instance termination before any subnet/SG removal.
			c.Prior = r.Resource
			if r, err = p.destroy(context.Background(), a, c); err != nil || !r.Absent {
				t.Fatal("router cleanup", err)
			}
			c.Prior = network.Resource
			if r, err = p.destroyNetwork(context.Background(), a, c); err != nil || !r.Absent || len(f.objects) != 0 {
				t.Fatal("network cleanup", err)
			}
		})
	}
}
func TestInvalidTopologyNeverProvisions(t *testing.T) {
	f, a := newNetworkEC2(t)
	p := &Provider{Journal: journal.Journal{Root: t.TempDir()}}
	c := twoNetworks()
	c.Source.Lab.Machines["second-router"] = c.Source.Lab.Machines["router"]
	if _, err := p.network(context.Background(), a, c); err == nil {
		t.Fatal("ambiguous topology accepted")
	}
	if len(f.requests) != 0 {
		t.Fatal("validated after mutation", f.requests)
	}
}

func TestAuthorLayoutLostFirstSubnetRecoversAndCleansUp(t *testing.T) {
	f, a := newNetworkEC2(t)
	f.loss = "CreateSubnet:192.168.2.0/24"
	p := &Provider{Journal: journal.Journal{Root: t.TempDir()}}
	c := twoNetworks()
	c.Source.Lab.Networks = map[string]lc.Network{"a": {CIDR: "192.168.2.0/24", Egress: "internet"}, "b": {CIDR: "192.168.255.240/28"}}
	if _, err := p.network(context.Background(), a, c); err == nil {
		t.Fatal("author subnet response loss accepted")
	}
	if _, err := p.findNetwork(context.Background(), a, c); err != nil {
		t.Fatal("author CIDR discovery used default CIDR", err)
	}
	r, err := p.network(context.Background(), a, c)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range f.requests {
		if v.Get("Action") == "CreateVpc" {
			requestValue(t, v, "CidrBlock", "192.168.0.0/16")
		}
		if v.Get("Action") == "CreateSubnet" && tagValue(v, "labcentral:component") == "edge-subnet" {
			requestValue(t, v, "CidrBlock", "192.168.255.224/28")
		}
	}
	c.Prior = r.Resource
	if r, err = p.destroyNetwork(context.Background(), a, c); err != nil || !r.Absent || len(f.objects) != 0 {
		t.Fatal("author CIDR cleanup", err)
	}
}

func TestCheckQueriesActualENILimitWhenEnvironmentOmitsIt(t *testing.T) {
	for _, configured := range []int{0, 4} {
		t.Run(fmt.Sprint(configured), func(t *testing.T) {
			c := twoNetworks()
			c.Environment.AWS.DesktopProfile = ""
			c.Environment.AWS.Types[0].MaxNICs = configured
			queries := 0
			api := ec2.New(ec2.Options{Region: "us-east-1", Credentials: credentials.NewStaticCredentialsProvider("test", "test", ""), RetryMaxAttempts: 1, HTTPClient: optionHTTPClient(func(r *http.Request) (*http.Response, error) {
				if err := r.ParseForm(); err != nil {
					t.Fatal(err)
				}
				action := r.Form.Get("Action")
				body := ""
				switch action {
				case "DescribeAvailabilityZones":
					body = "<availabilityZoneInfo><item><zoneName>us-east-1a</zoneName><zoneState>available</zoneState></item></availabilityZoneInfo>"
				case "DescribeImages":
					body = "<imagesSet><item><imageId>ami-base</imageId><imageState>available</imageState><architecture>x86_64</architecture></item></imagesSet>"
				case "DescribeInstanceTypes":
					queries++
					body = "<instanceTypeSet>"
					for _, name := range []string{"t3.micro", "t3.large"} {
						body += "<item><instanceType>" + name + "</instanceType><processorInfo><supportedArchitectures><item>x86_64</item></supportedArchitectures></processorInfo><vCpuInfo><defaultVCpus>2</defaultVCpus></vCpuInfo><memoryInfo><sizeInMiB>8192</sizeInMiB></memoryInfo><networkInfo><maximumNetworkInterfaces>1</maximumNetworkInterfaces></networkInfo></item>"
					}
					body += "</instanceTypeSet>"
				default:
					t.Error("unexpected native action", action)
				}
				return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/xml"}}, Body: io.NopCloser(strings.NewReader("<" + action + "Response>" + body + "</" + action + "Response>"))}, nil
			})})
			_, err := (&Provider{}).check(context.Background(), api, c)
			typed, ok := err.(*lc.Error)
			if !ok || typed.Code != lc.Unsupported || typed.Field != "machines.router.nics[1]" || queries != 1 {
				t.Fatal("actual ENI limit not checked", queries, err)
			}
		})
	}
}
