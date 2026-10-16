package aws

import (
	"errors"

	"github.com/aws/smithy-go"
)

// Only documented denials that did not create a resource may clear an intent.
// Keep operation-specific capacity/validation errors scoped to their native action;
// an unknown code, transport error or server failure never proves rejection.
// https://docs.aws.amazon.com/ec2/latest/devguide/errors-overview.html
func definiteEC2Rejection(action, code string) bool {
	switch code {
	case "UnauthorizedOperation", "AuthFailure", "InvalidParameterValue", "InvalidParameterCombination", "MissingParameter", "RequestLimitExceeded", "Throttling", "ThrottlingException":
		return true
	}
	var codes []string
	switch action {
	case "RunInstances":
		codes = []string{"InstanceLimitExceeded", "VcpuLimitExceeded", "InsufficientFreeAddressesInSubnet", "InsufficientInstanceCapacity", "InsufficientHostCapacity", "InsufficientCapacityOnHost", "InsufficientReservedInstanceCapacity", "NetworkInterfaceLimitExceeded", "VolumeLimitExceeded", "AttachmentLimitExceeded", "InvalidAMIID.NotFound", "InvalidAMIID.Malformed", "InvalidAMIID.Unavailable", "InvalidSubnetID.NotFound", "InvalidGroup.NotFound", "InvalidKeyPair.NotFound", "InvalidBlockDeviceMapping", "Unsupported", "UnsupportedOperation", "OptInRequired", "PendingVerification"}
	case "CreateImage":
		codes = []string{"InvalidAMIName.Duplicate", "InvalidAMIName.Malformed", "InvalidInstanceID.NotFound", "IncorrectInstanceState", "ImageLimitExceeded", "SnapshotLimitExceeded"}
	case "CreateVpc":
		codes = []string{"VpcLimitExceeded"}
	case "CreateSubnet":
		codes = []string{"SubnetLimitExceeded", "InvalidSubnet.Conflict"}
	case "CreateSecurityGroup":
		codes = []string{"SecurityGroupLimitExceeded", "InvalidGroup.Duplicate"}
	case "CreateRouteTable":
		codes = []string{"RouteTableLimitExceeded"}
	case "CreateInternetGateway":
		codes = []string{"InternetGatewayLimitExceeded"}
	case "CreateVpcEndpoint":
		codes = []string{"VpcEndpointLimitExceeded"}
	case "ImportKeyPair":
		codes = []string{"KeyPairLimitExceeded", "InvalidKeyPair.Duplicate"}
	}
	for _, denied := range codes {
		if code == denied {
			return true
		}
	}
	return false
}
func rejectedEC2Error(err error) bool {
	var operation *smithy.OperationError
	var api smithy.APIError
	if !errors.As(err, &api) {
		return false
	}
	action := ""
	if errors.As(err, &operation) {
		action = operation.OperationName
	}
	return definiteEC2Rejection(action, api.ErrorCode())
}
