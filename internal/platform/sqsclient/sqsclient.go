package sqsclient

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"go.uber.org/fx"

	"github.com/RobertoPSF/backend-challenge-go/internal/platform/config"
)

var Module = fx.Module("sqs", fx.Provide(New))

type Client struct {
	*sqs.Client
	Queues Queues
}

type Queues struct {
	InputURL    string
	InputDLQURL string
	EventsURL   string
}

func New(lc fx.Lifecycle, cfg config.Config, log *slog.Logger) (*Client, error) {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	awsCfg, err := awsconfig.LoadDefaultConfig(context.Background(),
		awsconfig.WithRegion(cfg.AWS.Region),
		awsconfig.WithHTTPClient(&http.Client{Transport: transport}),
	)
	if err != nil {
		return nil, fmt.Errorf("load aws config: %w", err)
	}

	client := &Client{Client: sqs.NewFromConfig(awsCfg, func(o *sqs.Options) {
		if cfg.AWS.EndpointURL != "" {
			o.BaseEndpoint = aws.String(cfg.AWS.EndpointURL)
		}
	})}

	lc.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			var err error
			if client.Queues.InputURL, err = client.queueURL(ctx, cfg.AWS.InputQueue); err != nil {
				return err
			}
			if client.Queues.EventsURL, err = client.queueURL(ctx, cfg.AWS.EventsQueue); err != nil {
				return err
			}
			if client.Queues.InputDLQURL, err = client.queueURL(ctx, cfg.AWS.InputDLQ); err != nil {
				return err
			}
			log.Info("sqs queues resolved", "input", cfg.AWS.InputQueue, "events", cfg.AWS.EventsQueue)
			return nil
		},
		OnStop: func(context.Context) error {
			transport.CloseIdleConnections()
			log.Info("sqs client closed")
			return nil
		},
	})
	return client, nil
}

func (c *Client) queueURL(ctx context.Context, name string) (string, error) {
	out, err := c.GetQueueUrl(ctx, &sqs.GetQueueUrlInput{QueueName: aws.String(name)})
	if err != nil {
		return "", fmt.Errorf("resolve sqs queue %q: %w", name, err)
	}
	return aws.ToString(out.QueueUrl), nil
}
