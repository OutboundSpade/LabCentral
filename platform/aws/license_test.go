package aws

import (
	"net/url"
	"testing"
)

func TestLicensingPolicyRejectsManagementCredentials(t *testing.T) {
	good := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"s3:GetObject","Resource":"arn:aws:s3:::dcv-license.us-east-1/*"}]}`
	if err := validateLicensePolicy(url.QueryEscape(good), "us-east-1"); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{
		`{"Statement":[{"Effect":"Allow","Action":"*","Resource":"*"}]}`,
		`{"Statement":[{"Effect":"Allow","NotAction":"s3:GetObject","Resource":"*"}]}`,
		`{"Statement":[{"Effect":"Allow","Action":["s3:GetObject","ec2:*"],"Resource":"arn:aws:s3:::dcv-license.us-east-1/*"}]}`,
	} {
		if err := validateLicensePolicy(bad, "us-east-1"); err == nil {
			t.Fatal("broad role accepted")
		}
	}
}
