package aws

import (
	"context"
	"encoding/json"
	"fmt"
	lc "github.com/OutboundSpade/LabCentral/v2"
	aw "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"net/url"
	"strings"
)

type stringsOrOne []string

func (s *stringsOrOne) UnmarshalJSON(b []byte) error {
	var one string
	if json.Unmarshal(b, &one) == nil {
		*s = []string{one}
		return nil
	}
	var many []string
	if err := json.Unmarshal(b, &many); err != nil {
		return err
	}
	*s = many
	return nil
}

// Require a dedicated inline-only role. Unknown policy shapes fail closed.
func validateLicensePolicy(raw, region string) error {
	var policy struct {
		Version   string
		Statement []struct {
			Sid      string
			Effect   string
			Action   stringsOrOne
			Resource stringsOrOne
		}
	}
	text, err := url.QueryUnescape(raw)
	if err != nil {
		return err
	}
	dec := json.NewDecoder(strings.NewReader(text))
	dec.DisallowUnknownFields()
	if err = dec.Decode(&policy); err != nil {
		return fmt.Errorf("DCV role policy has unsupported fields: %w", err)
	}
	if len(policy.Statement) != 1 {
		return fmt.Errorf("DCV role requires exactly one licensing statement")
	}
	s := policy.Statement[0]
	if s.Effect != "Allow" || len(s.Action) != 1 || s.Action[0] != "s3:GetObject" || len(s.Resource) != 1 || s.Resource[0] != "arn:aws:s3:::dcv-license."+region+"/*" {
		return fmt.Errorf("DCV role must grant only GetObject on this region's licensing bucket")
	}
	return nil
}
func checkDesktopRole(ctx context.Context, e lc.Environment) error {
	if e.AWS.DesktopProfile == "" {
		return nil
	}
	opts := []func(*config.LoadOptions) error{config.WithRegion(e.AWS.Region)}
	if e.AWS.Profile != "" {
		opts = append(opts, config.WithSharedConfigProfile(e.AWS.Profile))
	}
	cfg, err := config.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return err
	}
	api := iam.NewFromConfig(cfg)
	profile, err := api.GetInstanceProfile(ctx, &iam.GetInstanceProfileInput{InstanceProfileName: aw.String(e.AWS.DesktopProfile)})
	if err != nil {
		return err
	}
	if profile.InstanceProfile == nil || len(profile.InstanceProfile.Roles) != 1 {
		return fmt.Errorf("DCV profile must contain exactly one dedicated role")
	}
	role := profile.InstanceProfile.Roles[0].RoleName
	attached, err := api.ListAttachedRolePolicies(ctx, &iam.ListAttachedRolePoliciesInput{RoleName: role})
	if err != nil {
		return err
	}
	if len(attached.AttachedPolicies) != 0 || attached.IsTruncated {
		return fmt.Errorf("DCV role must not have managed policies")
	}
	inline, err := api.ListRolePolicies(ctx, &iam.ListRolePoliciesInput{RoleName: role})
	if err != nil {
		return err
	}
	if len(inline.PolicyNames) != 1 || inline.IsTruncated {
		return fmt.Errorf("DCV role needs a single licensing inline policy")
	}
	policy, err := api.GetRolePolicy(ctx, &iam.GetRolePolicyInput{RoleName: role, PolicyName: &inline.PolicyNames[0]})
	if err != nil {
		return err
	}
	return validateLicensePolicy(aw.ToString(policy.PolicyDocument), e.AWS.Region)
}
