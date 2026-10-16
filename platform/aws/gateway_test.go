package aws

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	lc "github.com/OutboundSpade/LabCentral/v2"
	"github.com/OutboundSpade/LabCentral/v2/internal/journal"
	"github.com/OutboundSpade/LabCentral/v2/internal/sshtest"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
)

type networkObject struct{ kind, id, component, cidr, role string }
type networkEC2 struct {
	detached, enisDeleted, tagsRemoved map[string]bool
	owners                             map[string]string
	t                                  *testing.T
	objects                            []networkObject
	requests                           []url.Values
	loss, deleteLoss                   string
	public                             string
	terminated                         map[string]bool
	defaults                           map[string]string
	subnets                            map[string]string
	routes                             map[string]map[string]string
	instanceNICs                       map[string]url.Values
	routeReject                        string
	imagePlatform                      string
}

func newNetworkEC2(t *testing.T) (*networkEC2, *ec2.Client) {
	f := &networkEC2{t: t, terminated: map[string]bool{}, defaults: map[string]string{}, subnets: map[string]string{}, public: "192.0.2.5", routes: map[string]map[string]string{}, instanceNICs: map[string]url.Values{}}
	api := ec2.New(ec2.Options{Region: "us-east-1", Credentials: credentials.NewStaticCredentialsProvider("test", "test", ""), RetryMaxAttempts: 1, HTTPClient: optionHTTPClient(f.do)})
	return f, api
}
func tagValue(v url.Values, key string) string {
	for i := 1; i < 20; i++ {
		if v.Get(fmt.Sprintf("TagSpecification.1.Tag.%d.Key", i)) == key {
			return v.Get(fmt.Sprintf("TagSpecification.1.Tag.%d.Value", i))
		}
	}
	return ""
}
func fakeTags(role, component string) string {
	text := `<tagSet><item><key>labcentral:owner</key><value>owner</value></item><item><key>labcentral:version</key><value>2</value></item><item><key>labcentral:role</key><value>` + role + `</value></item>`
	if component != "" {
		text += `<item><key>labcentral:component</key><value>` + component + `</value></item>`
	}
	return text + `</tagSet>`
}
func (f *networkEC2) matching(v url.Values, o networkObject) bool {
	for key, values := range v {
		if strings.HasPrefix(key, "Filter.") && strings.HasSuffix(key, ".Name") {
			value := v.Get(strings.TrimSuffix(key, ".Name") + ".Value.1")
			switch values[0] {
			case "tag:labcentral:owner":
				if owner, ok := f.owners[o.id]; ok && owner != value {
					return false
				}
			case "tag:labcentral:component":
				if o.component != value {
					return false
				}
			case "tag:labcentral:role":
				if o.role != value {
					return false
				}
			case "cidr-block":
				if o.cidr != value {
					return false
				}
			}
		}
	}
	for _, key := range []string{"SubnetId.1", "RouteTableId.1", "GroupId.1", "InternetGatewayId.1", "VpcEndpointId.1", "VpcId.1", "InstanceId.1"} {
		if id := v.Get(key); id != "" && id != o.id {
			return false
		}
	}
	return true
}
func (f *networkEC2) do(r *http.Request) (*http.Response, error) {
	if err := r.ParseForm(); err != nil {
		f.t.Fatal(err)
	}
	v := r.Form
	f.requests = append(f.requests, v)
	action := v.Get("Action")
	body := "<return>true</return>"
	errorCode := ""
	createKinds := map[string]string{"CreateVpc": "vpc", "CreateSubnet": "subnet", "CreateInternetGateway": "igw", "CreateRouteTable": "route", "CreateSecurityGroup": "sg", "CreateVpcEndpoint": "endpoint", "RunInstances": "instance"}
	if kind := createKinds[action]; kind != "" {
		count := len(f.objects)
		for _, object := range f.objects {
			if owner, ok := f.owners[object.id]; ok && owner != "owner" {
				count--
			}
		}
		id := fmt.Sprintf("%s-%d", kind, count+1)
		component := tagValue(v, "labcentral:component")
		o := networkObject{kind: kind, id: id, component: component, cidr: v.Get("CidrBlock"), role: tagValue(v, "labcentral:role")}
		f.objects = append(f.objects, o)
		switch kind {
		case "vpc":
			body = "<vpc><vpcId>" + id + "</vpcId><state>available</state></vpc>"
		case "subnet":
			body = "<subnet><subnetId>" + id + "</subnetId></subnet>"
		case "igw":
			body = "<internetGateway><internetGatewayId>" + id + "</internetGatewayId></internetGateway>"
		case "route":
			body = "<routeTable><routeTableId>" + id + "</routeTableId></routeTable>"
		case "sg":
			body = "<groupId>" + id + "</groupId>"
		case "endpoint":
			body = "<vpcEndpoint><vpcEndpointId>" + id + "</vpcEndpointId></vpcEndpoint>"
		case "instance":
			f.instanceNICs[id] = v
			body = "<instancesSet><item><instanceId>" + id + "</instanceId></item></instancesSet>"
		}
		if f.loss == action+":"+component || f.loss == action+":"+o.cidr || f.loss == action+":"+o.role {
			f.loss = ""
			return nil, io.ErrUnexpectedEOF
		}
	} else {
		switch action {
		case "DescribeImages":
			body = "<imagesSet><item><imageId>ami-base</imageId><rootDeviceName>/dev/sda1</rootDeviceName><state>available</state>"
			if f.imagePlatform != "" {
				body += "<platform>" + f.imagePlatform + "</platform>"
			}
			body += "</item></imagesSet>"
		case "DescribePrefixLists":
			body = "<prefixListSet><item><prefixListId>pl-s3</prefixListId></item></prefixListSet>"
		case "DescribeNetworkInterfaces":
			body = "<networkInterfaceSet>"
			for instance, request := range f.instanceNICs {
				count := 1
				if request.Get("NetworkInterface.2.PrivateIpAddress") != "" {
					count = 2
				}
				match := true
				for key, values := range v {
					if strings.HasPrefix(key, "Filter.") && strings.HasSuffix(key, ".Name") && values[0] == "tag:labcentral:role" && v.Get(strings.TrimSuffix(key, ".Name")+".Value.1") != tagValue(request, "labcentral:role") {
						match = false
					}
				}
				if !match {
					continue
				}
				for index := 1; index <= count; index++ {
					id := "eni-gateway"
					if request.Get("NetworkInterface.1.PrivateIpAddress") != "" {
						id = "eni-" + instance + fmt.Sprint(index)
					}
					if f.enisDeleted[id] || (f.terminated[instance] && !f.detached[id]) {
						continue
					}
					if wanted := v.Get("NetworkInterfaceId.1"); wanted != "" && wanted != id {
						continue
					}
					nativeTags := ""
					if request.Get("TagSpecification.3.ResourceType") == "network-interface" {
						nativeTags = fakeTags(tagValue(request, "labcentral:role"), "")
					}
					if owner, ok := f.owners[id]; ok {
						nativeTags = strings.Replace(fakeTags("machine", ""), "<value>owner</value>", "<value>"+owner+"</value>", 1)
					}
					if f.tagsRemoved[id] {
						nativeTags = ""
					}
					if v.Get("NetworkInterfaceId.1") == "" {
						if owner, ok := f.owners[id]; ok && owner != "owner" {
							continue
						}
						if f.tagsRemoved[id] {
							continue
						}
					}
					attachment := "<status>available</status>"
					if !f.detached[id] {
						attachment = "<status>in-use</status><attachment><instanceId>" + instance + "</instanceId><deleteOnTermination>true</deleteOnTermination></attachment>"
					}
					body += "<item><networkInterfaceId>" + id + "</networkInterfaceId>" + attachment + nativeTags + "</item>"
				}
			}
			body += "</networkInterfaceSet>"
		case "DescribeVolumes":
			body = "<volumeSet/>"
		case "DescribeVpcs", "DescribeSubnets", "DescribeInternetGateways", "DescribeRouteTables", "DescribeSecurityGroups", "DescribeVpcEndpoints", "DescribeInstances":
			kind := map[string]string{"DescribeVpcs": "vpc", "DescribeSubnets": "subnet", "DescribeInternetGateways": "igw", "DescribeRouteTables": "route", "DescribeSecurityGroups": "sg", "DescribeVpcEndpoints": "endpoint", "DescribeInstances": "instance"}[action]
			set := map[string]string{"vpc": "vpcSet", "subnet": "subnetSet", "igw": "internetGatewaySet", "route": "routeTableSet", "sg": "securityGroupInfo", "endpoint": "vpcEndpointSet", "instance": "reservationSet"}[kind]
			body = "<" + set + ">"
			count := 0
			for _, o := range f.objects {
				if o.kind != kind || !f.matching(v, o) {
					continue
				}
				count++
				item := ""
				switch kind {
				case "vpc":
					item = "<vpcId>" + o.id + "</vpcId><state>available</state>"
				case "subnet":
					item = "<subnetId>" + o.id + "</subnetId><cidrBlock>" + o.cidr + "</cidrBlock><vpcId>vpc-1</vpcId>"
				case "igw":
					item = "<internetGatewayId>" + o.id + "</internetGatewayId><attachmentSet><item><vpcId>vpc-1</vpcId></item></attachmentSet>"
				case "route":
					item = "<routeTableId>" + o.id + "</routeTableId><vpcId>vpc-1</vpcId><associationSet><item><subnetId>" + f.subnets[o.id] + "</subnetId></item></associationSet><routeSet>"
					if dest := f.defaults[o.id]; dest != "" {
						target := "gatewayId"
						if strings.HasPrefix(dest, "eni-") {
							target = "networkInterfaceId"
						}
						item += "<item><destinationCidrBlock>0.0.0.0/0</destinationCidrBlock><" + target + ">" + dest + "</" + target + "></item>"
					}
					for destination, target := range f.routes[o.id] {
						item += "<item><destinationCidrBlock>" + destination + "</destinationCidrBlock><networkInterfaceId>" + target + "</networkInterfaceId></item>"
					}
					item += "</routeSet>"
				case "sg":
					item = "<groupId>" + o.id + "</groupId><vpcId>vpc-1</vpcId><ipPermissionsEgress><item><ipProtocol>-1</ipProtocol><ipRanges><item><cidrIp>0.0.0.0/0</cidrIp></item></ipRanges></item></ipPermissionsEgress>"
				case "endpoint":
					item = "<vpcEndpointId>" + o.id + "</vpcEndpointId><vpcId>vpc-1</vpcId>"
				case "instance":
					item = "<instanceId>" + o.id + "</instanceId><instanceState><name>running</name><code>16</code></instanceState><ipAddress>" + f.public + "</ipAddress><privateIpAddress>10.77.0.11</privateIpAddress><networkInterfaceSet><item><networkInterfaceId>eni-gateway</networkInterfaceId></item></networkInterfaceSet>"
				}
				if kind == "instance" && strings.HasPrefix(o.role, "machine:") {
					request := f.instanceNICs[o.id]
					if request.Get("NetworkInterface.1.PrivateIpAddress") != "" {
						enis := ""
						for j := 1; j <= 8; j++ {
							prefix := fmt.Sprintf("NetworkInterface.%d.", j)
							address := request.Get(prefix + "PrivateIpAddress")
							if address == "" {
								break
							}
							if f.detached["eni-"+o.id+fmt.Sprint(j)] {
								continue
							}
							enis += "<item><networkInterfaceId>eni-" + o.id + fmt.Sprint(j) + "</networkInterfaceId><vpcId>vpc-1</vpcId><subnetId>" + request.Get(prefix+"SubnetId") + "</subnetId><privateIpAddress>" + address + "</privateIpAddress><attachment><deviceIndex>" + request.Get(prefix+"DeviceIndex") + "</deviceIndex><deleteOnTermination>true</deleteOnTermination></attachment></item>"
						}
						a := strings.Index(item, "<networkInterfaceSet>")
						b := strings.Index(item, "</networkInterfaceSet>") + len("</networkInterfaceSet>")
						item = item[:a] + "<networkInterfaceSet>" + enis + "</networkInterfaceSet>" + item[b:]
						item = strings.Replace(item, "<privateIpAddress>10.77.0.11</privateIpAddress>", "<privateIpAddress>"+request.Get("NetworkInterface.1.PrivateIpAddress")+"</privateIpAddress>", 1)
					}
				}
				nativeTags := fakeTags(o.role, o.component)
				if owner, ok := f.owners[o.id]; ok {
					nativeTags = strings.Replace(nativeTags, "<value>owner</value>", "<value>"+owner+"</value>", 1)
				}
				if kind == "instance" && f.instanceNICs[o.id] == nil {
					start := strings.Index(item, "<networkInterfaceSet>")
					end := strings.Index(item, "</networkInterfaceSet>")
					if start >= 0 && end >= 0 {
						item = item[:start] + item[end+len("</networkInterfaceSet>"):]
					}
				}
				item += nativeTags
				if kind == "instance" {
					item = "<instancesSet><item>" + item + "</item></instancesSet>"
				}
				body += "<item>" + item + "</item>"
			}
			body += "</" + set + ">"
			if count == 0 && v.Get("VpcId.1") != "" {
				errorCode = "InvalidVpcID.NotFound"
			}
			if count == 0 && v.Get("InstanceId.1") != "" {
				if f.terminated[v.Get("InstanceId.1")] {
					body = "<reservationSet><item><instancesSet><item><instanceId>" + v.Get("InstanceId.1") + "</instanceId><instanceState><name>terminated</name><code>48</code></instanceState>" + fakeTags("gateway", "") + "</item></instancesSet></item></reservationSet>"
				} else {
					errorCode = "InvalidInstanceID.NotFound"
				}
			}
		case "DeleteNetworkInterface":
			if f.enisDeleted == nil {
				f.enisDeleted = map[string]bool{}
			}
			f.enisDeleted[v.Get("NetworkInterfaceId")] = true
		case "CreateRoute":
			target := v.Get("GatewayId")
			if target == "" {
				target = v.Get("NetworkInterfaceId")
			}
			if destination := v.Get("DestinationCidrBlock"); destination == "0.0.0.0/0" {
				f.defaults[v.Get("RouteTableId")] = target
			} else {
				if f.routeReject != "" {
					errorCode = f.routeReject
				} else {
					if f.routes[v.Get("RouteTableId")] == nil {
						f.routes[v.Get("RouteTableId")] = map[string]string{}
					}
					f.routes[v.Get("RouteTableId")][destination] = target
				}
			}
		case "AssociateRouteTable":
			f.subnets[v.Get("RouteTableId")] = v.Get("SubnetId")
		case "AttachInternetGateway", "DetachInternetGateway", "AuthorizeSecurityGroupIngress", "AuthorizeSecurityGroupEgress", "RevokeSecurityGroupEgress", "RevokeSecurityGroupIngress", "ModifyInstanceAttribute", "ModifyNetworkInterfaceAttribute":
		case "DeleteRoute":
			if destination := v.Get("DestinationCidrBlock"); destination == "0.0.0.0/0" {
				delete(f.defaults, v.Get("RouteTableId"))
			} else {
				delete(f.routes[v.Get("RouteTableId")], destination)
			}
		case "DeleteSubnet", "DeleteRouteTable", "DeleteSecurityGroup", "DeleteInternetGateway", "DeleteVpc", "DeleteVpcEndpoints", "TerminateInstances":
			// Like EC2, a subnet cannot be deleted while a more-specific route
			// to a router ENI still references it.
			if action == "DeleteSubnet" {
				blocked := false
				for _, routes := range f.routes {
					blocked = blocked || len(routes) > 0
				}
				if blocked {
					errorCode = "DependencyViolation"
					break
				}
			}
			id := ""
			for _, key := range []string{"SubnetId", "RouteTableId", "GroupId", "InternetGatewayId", "VpcId", "VpcEndpointId.1", "InstanceId.1"} {
				if v.Get(key) != "" {
					id = v.Get(key)
				}
			}
			if action == "TerminateInstances" {
				f.terminated[id] = true
			}
			for n, o := range f.objects {
				if o.id == id {
					f.objects = append(f.objects[:n], f.objects[n+1:]...)
					break
				}
			}
			if f.deleteLoss == action {
				f.deleteLoss = ""
				return nil, io.ErrUnexpectedEOF
			}
		default:
			f.t.Errorf("unexpected fake EC2 request: %s", action)
		}
	}
	status := 200
	if errorCode != "" {
		status = 400
		body = "<Errors><Error><Code>" + errorCode + "</Code><Message>absent</Message></Error></Errors>"
		action = "Response"
	}
	return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": []string{"text/xml"}}, Body: io.NopCloser(strings.NewReader("<" + action + "Response>" + body + "</" + action + "Response>"))}, nil
}
func networkCommand(internet bool) lc.Command {
	c := lc.Command{Operation: "owner/network", Owner: "owner", Environment: lc.Environment{Platform: "aws", Bases: map[string]lc.Base{"ubuntu-24.04": {ID: "ami-base", Identity: "ami-base", DiskGiB: 8}}, OperatorCIDRs: []string{"192.0.2.10/32"}, AWS: lc.AWSConfig{Region: "us-east-1", Zone: "us-east-1a", DesktopProfile: "license", AllowEgress: internet}}}
	if internet {
		c.Source.Lab.Networks = map[string]lc.Network{"lab": {Egress: "internet"}}
	}
	return c
}
func requestValue(t *testing.T, v url.Values, key, want string) {
	t.Helper()
	if v.Get(key) != want {
		t.Fatalf("%s: got %q want %q (%s)", key, v.Get(key), want, v.Get("Action"))
	}
}
func TestPrivateLayoutAndGatewayRequests(t *testing.T) {
	for _, internet := range []bool{false, true} {
		t.Run(fmt.Sprint(internet), func(t *testing.T) {
			f, a := newNetworkEC2(t)
			p := &Provider{Journal: journal.Journal{Root: t.TempDir()}}
			c := networkCommand(internet)
			netResult, err := p.network(context.Background(), a, c)
			if err != nil {
				t.Fatal(err)
			}
			var ns networkState
			json.Unmarshal(netResult.Resource.Data, &ns)
			assertSecurityRules(t, f.requests, ns, internet)
			for _, v := range f.requests {
				switch v.Get("Action") {
				case "CreateVpc":
					requestValue(t, v, "CidrBlock", "10.77.0.0/16")
				case "CreateSubnet":
					want := "10.77.0.0/24"
					if tagValue(v, "labcentral:component") == "edge-subnet" {
						want = "10.77.255.0/28"
					}
					requestValue(t, v, "CidrBlock", want)
					requestValue(t, v, "AvailabilityZone", "us-east-1a")
				case "CreateRoute":
					requestValue(t, v, "RouteTableId", ns.EdgeRoute)
					requestValue(t, v, "GatewayId", ns.Gateway)
					requestValue(t, v, "DestinationCidrBlock", "0.0.0.0/0")
				case "CreateVpcEndpoint":
					requestValue(t, v, "RouteTableId.1", ns.Route)
				case "AuthorizeSecurityGroupIngress":
					if v.Get("GroupId") == ns.SG && v.Get("IpPermissions.1.IpProtocol") == "tcp" {
						requestValue(t, v, "IpPermissions.1.Groups.1.GroupId", ns.GatewaySG)
						requestValue(t, v, "IpPermissions.1.IpRanges.1.CidrIp", "")
					}
				}
			}
			key, pub := sshtest.Key(t)
			hp, hpub := sshtest.Key(t)
			sshServer := sshtest.New(t, nil, func(string) string { return "" })
			f.public = sshServer.Address
			gc := c
			gc.Operation = "owner/gateway"
			gc.Network = netResult.Resource
			gc.ClientPrivateKey = key
			gc.Machine = lc.PlannedMachine{Name: "gateway", NativeImage: "ami-base", InstanceType: "t3.micro", DiskGiB: 8, PublicKey: pub, HostPrivateKey: string(hp), HostPublicKey: sshServer.HostKey}
			_ = hpub
			result, err := p.gateway(context.Background(), a, gc)
			if err != nil {
				t.Fatal(err)
			}
			if result.Resource.Kind != "gateway" || !result.Resource.Complete {
				t.Fatal(result)
			}
			seenRun, seenModify := false, false
			for _, v := range f.requests {
				switch v.Get("Action") {
				case "RunInstances":
					seenRun = true
					for key, want := range map[string]string{"InstanceType": "t3.micro", "NetworkInterface.1.SubnetId": ns.EdgeSubnet, "NetworkInterface.1.SecurityGroupId.1": ns.GatewaySG, "NetworkInterface.1.AssociatePublicIpAddress": "true", "MetadataOptions.HttpTokens": "required", "MetadataOptions.HttpPutResponseHopLimit": "1", "IamInstanceProfile.Name": "", "BlockDeviceMapping.1.Ebs.Encrypted": "true"} {
						requestValue(t, v, key, want)
					}
					raw, err := base64.StdEncoding.DecodeString(v.Get("UserData"))
					if err != nil {
						t.Fatal(err)
					}
					text := string(raw)
					for _, want := range []string{"AllowTcpForwarding local", "AllowAgentForwarding no", "PermitTunnel no", "X11Forwarding no", "PasswordAuthentication no", "net.ipv4.ip_forward=1", "nft -f", "169.254.0.0/16", "100.64.0.0/10", strings.TrimSpace(pub), strings.TrimSpace(sshServer.HostKey)} {
						if !strings.Contains(text, want) {
							t.Fatal("missing gateway bootstrap", want)
						}
					}
					if strings.Contains(text, "masquerade") != internet {
						t.Fatal("wrong NAT policy", internet)
					}
				case "ModifyInstanceAttribute", "ModifyNetworkInterfaceAttribute":
					seenModify = true
					requestValue(t, v, "SourceDestCheck.Value", "false")
				}
			}
			if !seenRun || !seenModify {
				t.Fatal("gateway lifecycle missing")
			}
			if internet {
				if f.defaults[ns.Route] != "eni-gateway" {
					t.Fatal("lab route not through gateway", f.defaults)
				}
			} else if f.defaults[ns.Route] != "" {
				t.Fatal("default route on denied network")
			}
		})
	}
}

func TestNetworkChildrenLostResponsesReconcileWithoutRecreate(t *testing.T) {
	for _, loss := range []string{"CreateVpc:network", "CreateSubnet:10.77.0.0/24", "CreateInternetGateway:network", "CreateRouteTable:lab-route", "CreateSecurityGroup:lab-sg", "CreateSubnet:edge-subnet", "CreateRouteTable:edge-route", "CreateSecurityGroup:edge-sg", "CreateVpcEndpoint:network"} {
		t.Run(loss, func(t *testing.T) {
			f, a := newNetworkEC2(t)
			f.loss = loss
			p := &Provider{Journal: journal.Journal{Root: t.TempDir()}}
			c := networkCommand(false)
			if _, err := p.network(context.Background(), a, c); err == nil {
				t.Fatal("response loss accepted")
			}
			if _, err := p.findNetwork(context.Background(), a, c); err != nil {
				t.Fatal("discovery failed", err)
			}
			result, err := p.network(context.Background(), a, c)
			if err != nil {
				t.Fatal(err)
			}
			if len(f.objects) != 9 {
				t.Fatal("duplicate/missing network resources", len(f.objects))
			}
			c.Prior = result.Resource
			if r, err := p.destroyNetwork(context.Background(), a, c); err != nil || !r.Absent {
				t.Fatal("cleanup", err)
			}
			if len(f.objects) != 0 {
				t.Fatal("leaked network children", f.objects)
			}
		})
	}
}
func TestGatewayLostResponseDiscoveryAndInterruptedDeletion(t *testing.T) {
	f, a := newNetworkEC2(t)
	f.loss = "RunInstances:gateway"
	p := &Provider{Journal: journal.Journal{Root: t.TempDir()}}
	c := networkCommand(false)
	network, err := p.network(context.Background(), a, c)
	if err != nil {
		t.Fatal(err)
	}
	key, pub := sshtest.Key(t)
	hp, _ := sshtest.Key(t)
	server := sshtest.New(t, nil, nil)
	f.public = server.Address
	c.Operation = "owner/gateway"
	c.Network = network.Resource
	c.ClientPrivateKey = key
	c.Machine = lc.PlannedMachine{Name: "gateway", NativeImage: "ami-base", InstanceType: "t3.micro", DiskGiB: 8, PublicKey: pub, HostPrivateKey: string(hp), HostPublicKey: server.HostKey}
	if _, err = p.gateway(context.Background(), a, c); err == nil {
		t.Fatal("lost gateway response accepted")
	}
	r, err := p.findGateway(context.Background(), a, c)
	if err != nil || r.Resource.ID == "" {
		t.Fatal("gateway discovery", err)
	}
	c.Recovery = true
	r, err = p.gateway(context.Background(), a, c)
	if err != nil {
		t.Fatal("gateway recovery", err)
	}
	runs := 0
	for _, v := range f.requests {
		if v.Get("Action") == "RunInstances" {
			runs++
		}
	}
	if runs != 1 {
		t.Fatal("duplicate gateway", runs)
	}
	c.Prior = r.Resource
	f.deleteLoss = "TerminateInstances"
	if _, err = p.destroy(context.Background(), a, c); err == nil {
		t.Fatal("lost termination accepted")
	}
	if r, err = p.destroy(context.Background(), a, c); err != nil || !r.Absent {
		t.Fatal("gateway deletion recovery", err)
	}
}

func TestNetworkInterruptedDeletesRetainAndRecoverChildren(t *testing.T) {
	for _, loss := range []string{"DeleteVpcEndpoints", "DeleteSubnet", "DeleteRouteTable", "DeleteSecurityGroup", "DeleteInternetGateway", "DeleteVpc"} {
		t.Run(loss, func(t *testing.T) {
			f, a := newNetworkEC2(t)
			p := &Provider{Journal: journal.Journal{Root: t.TempDir()}}
			c := networkCommand(false)
			result, err := p.network(context.Background(), a, c)
			if err != nil {
				t.Fatal(err)
			}
			c.Prior = result.Resource
			f.deleteLoss = loss
			if _, err = p.destroyNetwork(context.Background(), a, c); err == nil {
				t.Fatal("unconfirmed deletion accepted")
			}
			if r, err := p.destroyNetwork(context.Background(), a, c); err != nil || !r.Absent {
				t.Fatal("delete did not reconcile", err)
			}
			if len(f.objects) != 0 {
				t.Fatal("children leaked", f.objects)
			}
		})
	}
}
func TestUncertainEdgeCreateWithEmptyDiscoveryDoesNotRecreate(t *testing.T) {
	f, a := newNetworkEC2(t)
	f.loss = "CreateSubnet:edge-subnet"
	p := &Provider{Journal: journal.Journal{Root: t.TempDir()}}
	c := networkCommand(false)
	if _, err := p.network(context.Background(), a, c); err == nil {
		t.Fatal("response loss accepted")
	}
	// Simulate delayed visibility of the just-created resource, not confirmed absence.
	f.objects = f.objects[:len(f.objects)-1]
	for range 2 {
		if _, err := p.network(context.Background(), a, c); err == nil {
			t.Fatal("unknown outcome treated as failure")
		}
	}
	creates := 0
	for _, v := range f.requests {
		if v.Get("Action") == "CreateSubnet" && tagValue(v, "labcentral:component") == "edge-subnet" {
			creates++
		}
	}
	if creates != 1 {
		t.Fatal("uncertain resource was replaced", creates)
	}
}
func TestEgressDeniedBeforeAWSConfiguration(t *testing.T) {
	for _, action := range []string{"check", "network.create", "gateway.create", "machine.create", "image.build"} {
		_, err := (&Provider{}).Execute(context.Background(), lc.Command{Action: action, Environment: lc.Environment{Platform: "aws"}, Source: lc.Source{Lab: lc.Lab{Networks: map[string]lc.Network{"lab": {Egress: "internet"}}}}}, nil)
		if e, ok := err.(*lc.Error); !ok || e.Field != "networks.lab.egress" {
			t.Fatal("reached AWS configuration", action, err)
		}
	}
}

func TestMachineReadinessUsesGatewayAndPrivateManagement(t *testing.T) {
	f, a := newNetworkEC2(t)
	p := &Provider{Journal: journal.Journal{Root: t.TempDir()}}
	c := networkCommand(false)
	network, err := p.network(context.Background(), a, c)
	if err != nil {
		t.Fatal(err)
	}
	machine := sshtest.New(t, nil, nil)
	gateway := sshtest.New(t, func(dest string) string {
		if dest == "10.77.0.11:22" {
			return machine.Address
		}
		return ""
	}, nil)
	key, pub := sshtest.Key(t)
	hp, _ := sshtest.Key(t)
	f.public = gateway.Address
	c.Operation = "owner/gateway"
	c.Network = network.Resource
	c.ClientPrivateKey = key
	c.Machine = lc.PlannedMachine{Name: "gateway", NativeImage: "ami-base", InstanceType: "t3.micro", DiskGiB: 8, PublicKey: pub, HostPrivateKey: string(hp), HostPublicKey: gateway.HostKey}
	result, err := p.gateway(context.Background(), a, c)
	if err != nil {
		t.Fatal(err)
	}
	c.Gateway = result.Resource
	c.Action = "machine.create"
	c.Operation = "owner/machine/gateway"
	// An author machine named gateway has a distinct ownership role and host key.
	c.Machine = lc.PlannedMachine{Name: "gateway", NativeImage: "ami-base", InstanceType: "t3.medium", DiskGiB: 8, Address: "10.77.0.11", PublicKey: pub, HostPrivateKey: string(hp), HostPublicKey: machine.HostKey}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	r, err := p.machine(ctx, a, c)
	if err != nil {
		t.Fatal(err)
	}
	if r.Resource.Address != "10.77.0.11" || r.Resource.PrivateAddress != r.Resource.Address {
		t.Fatal("recorded public management address", r.Resource)
	}
	if gateway.Channels.Load() != 1 || machine.Connections.Load() != 1 {
		t.Fatal("readiness bypassed gateway")
	}
	if _, err = p.destroy(ctx, a, lc.Command{Owner: c.Owner, Prior: r.Resource}); err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, o := range f.objects {
		if o.kind == "instance" && o.role == "gateway" {
			count++
		}
	}
	if count != 1 {
		t.Fatal("author machine cleanup confused gateway ownership")
	}
}

func assertSecurityRules(t *testing.T, requests []url.Values, n networkState, internet bool) {
	t.Helper()
	rule := func(proto, group, cidr, port, prefix string) string {
		values := url.Values{"IpPermissions.1.IpProtocol": []string{proto}}
		if group != "" {
			values.Set("IpPermissions.1.Groups.1.GroupId", group)
		}
		if cidr != "" {
			values.Set("IpPermissions.1.IpRanges.1.CidrIp", cidr)
		}
		if port != "" {
			values.Set("IpPermissions.1.FromPort", port)
			values.Set("IpPermissions.1.ToPort", port)
		}
		if prefix != "" {
			values.Set("IpPermissions.1.PrefixListIds.1.PrefixListId", prefix)
		}
		return values.Encode()
	}
	expected := map[string][]string{
		"AuthorizeSecurityGroupIngress:" + n.SG:        {rule("-1", n.SG, "", "", ""), rule("tcp", n.GatewaySG, "", "22", ""), rule("tcp", n.GatewaySG, "", "8443", "")},
		"AuthorizeSecurityGroupIngress:" + n.GatewaySG: {rule("-1", n.SG, "", "", ""), rule("tcp", "", "192.0.2.10/32", "22", "")},
		"AuthorizeSecurityGroupEgress:" + n.SG:         {rule("-1", n.SG, "", "", ""), rule("tcp", "", "", "443", "pl-s3")},
		"AuthorizeSecurityGroupEgress:" + n.GatewaySG:  {rule("-1", "", "0.0.0.0/0", "", "")},
	}
	if internet {
		key := "AuthorizeSecurityGroupEgress:" + n.SG
		expected[key] = append(expected[key], rule("-1", "", "0.0.0.0/0", "", ""))
	}
	actual := map[string][]string{}
	for _, request := range requests {
		if !strings.HasPrefix(request.Get("Action"), "AuthorizeSecurityGroup") {
			continue
		}
		values := url.Values{}
		for key, value := range request {
			if strings.HasPrefix(key, "IpPermissions.") {
				values[key] = value
			}
		}
		key := request.Get("Action") + ":" + request.Get("GroupId")
		actual[key] = append(actual[key], values.Encode())
	}
	for _, values := range actual {
		sort.Strings(values)
	}
	for _, values := range expected {
		sort.Strings(values)
	}
	if !reflect.DeepEqual(actual, expected) {
		t.Fatalf("security rules differ:\ngot %#v\nwant %#v", actual, expected)
	}
}
