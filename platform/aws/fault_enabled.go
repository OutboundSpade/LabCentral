//go:build faultinject

package aws

import (
	"context"
	"github.com/OutboundSpade/LabCentral/v2/internal/faultinject"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/smithy-go"
	"github.com/aws/smithy-go/middleware"
)

func faultOptions(o *ec2.Options) {
	o.APIOptions = append(o.APIOptions, func(stack *middleware.Stack) error {
		return stack.Initialize.Add(middleware.InitializeMiddlewareFunc("LabCentralFault", func(ctx context.Context, in middleware.InitializeInput, next middleware.InitializeHandler) (out middleware.InitializeOutput, metadata middleware.Metadata, err error) {
			out, err = faultinject.Call(ctx, stack.ID(), func(ctx context.Context) (middleware.InitializeOutput, error) {
				var value middleware.InitializeOutput
				var err error
				value, metadata, err = next.HandleInitialize(ctx, in)
				return value, err
			})
			if status := faultinject.Status(err); status != 0 {
				code := map[int]string{403: "UnauthorizedOperation", 400: "InvalidParameterValue", 429: "RequestLimitExceeded", 500: "InternalError"}[status]
				err = &smithy.GenericAPIError{Code: code, Message: err.Error()}
			}
			return
		}), middleware.Before)
	})
}
