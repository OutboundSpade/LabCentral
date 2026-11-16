package aws

import (
	"context"
	"encoding/base64"
	"fmt"
	"regexp"
	"strings"
	"time"

	lc "github.com/OutboundSpade/LabCentral/v2"
	"github.com/OutboundSpade/LabCentral/v2/internal/guest"
	"github.com/OutboundSpade/LabCentral/v2/internal/remote"
	aw "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	t "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"golang.org/x/crypto/ssh"
)

func applianceCommands(c lc.Command) ([]string, error) {
	i := c.Source.Lab.Images[c.Machine.Spec.Image]
	if i.ApplianceKind() != c.Machine.Appliance || c.Environment.Platform != "aws" {
		return nil, fmt.Errorf("appliance command differs from source")
	}
	base := c.Environment.Bases[i.Base]
	if base.Appliance != c.Machine.Appliance || base.ID != c.Machine.NativeImage {
		return nil, fmt.Errorf("native appliance differs from the operator base binding")
	}
	if i.Appliance.Configuration == "" {
		return nil, nil
	}
	for _, f := range c.Source.Files {
		if f.Path == i.Appliance.Configuration {
			return lc.ApplianceCommands(c.Machine.Appliance, f.Content)
		}
	}
	return nil, fmt.Errorf("appliance configuration is unavailable")
}

var consoleMD5 = regexp.MustCompile(`(?i)\b(?:[0-9a-f]{2}:){15}[0-9a-f]{2}\b`)

func consoleHostPins(kind, owner, output string) (string, error) {
	if len(output) > 1<<20 {
		return "", fmt.Errorf("console output exceeds identity bound")
	}
	var pins []string
	if kind == "vyos" {
		prefix := "LABCENTRAL_HOST_KEY " + owner + " "
		for _, line := range strings.Split(output, "\n") {
			start := strings.Index(line, prefix)
			if start < 0 {
				continue
			}
			pub, _, _, _, err := ssh.ParseAuthorizedKey([]byte(strings.TrimSpace(line[start+len(prefix):])))
			if err == nil {
				pin := ssh.FingerprintSHA256(pub)
				if !lc.Contains(pins, pin) {
					pins = append(pins, pin)
				}
			}
		}
	} else if kind == "panos" {
		lower := strings.ToLower(output)
		start := strings.LastIndex(lower, "ssh public key fingerprints")
		if start < 0 {
			return "", nil
		}
		output = output[start:]
		if len(output) > 4096 {
			output = output[:4096]
		}
		for _, fp := range consoleMD5.FindAllString(output, -1) {
			pin := "MD5:" + strings.ToLower(fp)
			if !lc.Contains(pins, pin) {
				pins = append(pins, pin)
			}
		}
	} else {
		return "", fmt.Errorf("unknown native appliance")
	}
	if len(pins) > 16 {
		return "", fmt.Errorf("ambiguous appliance host identity")
	}
	return strings.Join(pins, ","), nil
}

func (p *Provider) appliancePins(ctx context.Context, a *ec2.Client, c lc.Command, id string) (string, error) {
	out, err := a.GetConsoleOutput(ctx, &ec2.GetConsoleOutputInput{InstanceId: &id, Latest: aw.Bool(true)})
	if err != nil {
		return "", err
	}
	if aw.ToString(out.InstanceId) != id {
		return "", fmt.Errorf("console identity differs from owned instance")
	}
	raw, err := base64.StdEncoding.DecodeString(aw.ToString(out.Output))
	if err != nil {
		return "", fmt.Errorf("invalid console identity encoding")
	}
	return consoleHostPins(c.Machine.Appliance, c.Owner, string(raw))
}

func (p *Provider) importApplianceKey(ctx context.Context, a *ec2.Client, c lc.Command) (string, error) {
	p.applianceKeyMu.Lock()
	defer p.applianceKeyMu.Unlock()
	name := "labcentral-appliance-" + c.Owner
	id, err := doEC2(ctx, p.Journal, c.Owner+"/appliance-key", func() (string, error) {
		r, err := a.ImportKeyPair(ctx, &ec2.ImportKeyPairInput{KeyName: &name, PublicKeyMaterial: []byte(c.Machine.PublicKey), TagSpecifications: specs(t.ResourceTypeKeyPair, c.Owner, "appliance-key")}, noCreateRetries)
		if err != nil {
			return "", err
		}
		return aw.ToString(r.KeyPairId), nil
	}, func() (string, bool, error) {
		r, err := a.DescribeKeyPairs(ctx, &ec2.DescribeKeyPairsInput{KeyNames: []string{name}, IncludePublicKey: aw.Bool(true)})
		if apiCode(err, "InvalidKeyPair.NotFound") {
			return "", false, nil
		}
		if err != nil {
			return "", false, err
		}
		if len(r.KeyPairs) != 1 || !owned(r.KeyPairs[0].Tags, c.Owner) || strings.TrimSpace(aw.ToString(r.KeyPairs[0].PublicKey)) != strings.TrimSpace(c.Machine.PublicKey) {
			return "", false, fmt.Errorf("appliance key ownership or material differs")
		}
		return aw.ToString(r.KeyPairs[0].KeyPairId), true, nil
	})
	if err != nil {
		return "", err
	}
	// A completed journal is not evidence that a same-named key still has its
	// original identity. Check immediately before using KeyName in a launch.
	current, err := a.DescribeKeyPairs(ctx, &ec2.DescribeKeyPairsInput{KeyNames: []string{name}, IncludePublicKey: aw.Bool(true)})
	if err != nil {
		return "", err
	}
	if len(current.KeyPairs) != 1 || !applianceKeyOwned(current.KeyPairs[0], c.Owner, name) || aw.ToString(current.KeyPairs[0].KeyPairId) != id || strings.TrimSpace(aw.ToString(current.KeyPairs[0].PublicKey)) != strings.TrimSpace(c.Machine.PublicKey) {
		return "", fmt.Errorf("appliance key identity changed")
	}
	return name, err
}

func applianceKeyOwned(key t.KeyPairInfo, owner, name string) bool {
	if !owned(key.Tags, owner) || aw.ToString(key.KeyName) != name || aw.ToString(key.KeyPairId) == "" {
		return false
	}
	for _, tag := range key.Tags {
		if aw.ToString(tag.Key) == "labcentral:role" {
			return aw.ToString(tag.Value) == "appliance-key"
		}
	}
	return false
}

func (p *Provider) cleanupApplianceKey(ctx context.Context, a *ec2.Client, c lc.Command) error {
	pending, err := p.Journal.HasIntent(c.Owner + "/appliance-key")
	if err != nil || !pending {
		return err
	}
	name := "labcentral-appliance-" + c.Owner
	r, err := a.DescribeKeyPairs(ctx, &ec2.DescribeKeyPairsInput{KeyNames: []string{name}})
	if apiCode(err, "InvalidKeyPair.NotFound") {
		return nil
	}
	if err != nil {
		return err
	}
	for _, key := range r.KeyPairs {
		if len(r.KeyPairs) != 1 || !applianceKeyOwned(key, c.Owner, name) {
			return fmt.Errorf("ownership_mismatch: appliance key retained")
		}
		var expected string
		loadErr := p.Journal.Load(c.Owner+"/appliance-key", &expected)
		if loadErr != nil && loadErr.Error() != "incomplete checkpoint" {
			return loadErr
		}
		if loadErr == nil && expected != aw.ToString(key.KeyPairId) {
			return fmt.Errorf("ownership_mismatch: replaced appliance key retained")
		}
		if _, err = a.DeleteKeyPair(ctx, &ec2.DeleteKeyPairInput{KeyPairId: key.KeyPairId}); err != nil {
			return err
		}
		check, err := a.DescribeKeyPairs(ctx, &ec2.DescribeKeyPairsInput{KeyPairIds: []string{aw.ToString(key.KeyPairId)}})
		if apiCode(err, "InvalidKeyPair.NotFound") {
			continue
		}
		if err != nil {
			return err
		}
		if len(check.KeyPairs) != 0 {
			return fmt.Errorf("appliance key deletion is unconfirmed")
		}
	}
	return nil
}

func (p *Provider) waitAppliance(ctx context.Context, a *ec2.Client, d *remote.Dialer, c lc.Command, r lc.Result) (lc.Result, error) {
	commands, err := applianceCommands(c)
	if err != nil {
		return r, err
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()
	for {
		if err := ctx.Err(); err != nil {
			return r, err
		}
		pin, err := p.appliancePins(ctx, a, c, r.Resource.ID)
		if err != nil {
			return r, err
		}
		if pin != "" {
			client, err := d.Dial(ctx, r.Resource.Address, c.Machine.SSHUser(), c.ClientPrivateKey, pin)
			if err == nil {
				if c.Machine.Appliance == "panos" {
					commands = append(guest.PANOSNetworkCommands(c.Machine), commands...)
					err = p.configurePANOS(ctx, client, c, commands)
				} else {
					var output string
					output, err = applianceCLI(ctx, client, []string{"show version"})
					if err == nil && !strings.Contains(output, "VyOS") {
						err = fmt.Errorf("native appliance did not report VyOS readiness")
					}
				}
				client.Close()
				if err != nil {
					return r, err
				}
				r.Resource.HostPublicKey = pin
				r.Resource.Complete = true
				return r, nil
			}
		}
		if err = waitRouteRetry(ctx, 3*time.Second); err != nil {
			return r, err
		}
	}
}
