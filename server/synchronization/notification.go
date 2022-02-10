package synchronization

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/carlmjohnson/requests"
	"github.com/jitsucom/jitsu/server/logging"
	"github.com/jitsucom/jitsu/server/meta"
	"github.com/mitchellh/mapstructure"
	"github.com/pkg/errors"
)

type LoggedTask struct {
	*meta.Task
	*TaskLogger
	Notifications map[string]interface{}
	Status        string
}

type NotificationContext struct {
	ServiceName string
	Version     string
	ServerName  string
	UIBaseURL   string
}

type NotificationChannel func(ctx context.Context, nctx *NotificationContext, configValue interface{}, task LoggedTask) error

type NotificationService struct {
	*NotificationContext
	globalConfig map[string]interface{}
	registry     map[string]NotificationChannel
}

func NewNotificationService(nctx *NotificationContext, config map[string]interface{}) *NotificationService {
	return &NotificationService{
		NotificationContext: nctx,
		globalConfig:        config,
		registry: map[string]NotificationChannel{
			"slack": Slack,
		},
	}
}

func (s *NotificationService) Notify(task LoggedTask) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	for key, value := range task.Notifications {
		if err := s.notify(ctx, key, value, task); err != nil {
			logging.Warnf("[%s] Failed to notify %s: %s", task.ID, key, err)
		}

		if ctx.Err() != nil {
			return
		}
	}

	for key, value := range s.globalConfig {
		if err := s.notify(ctx, key, value, task); err != nil {
			logging.Warnf("[%s] Failed to notify global %s: %s", task.ID, key, err)
		}

		if ctx.Err() != nil {
			return
		}
	}
}

func (s *NotificationService) notify(ctx context.Context, key string, config interface{}, task LoggedTask) error {
	notify, ok := s.registry[key]
	if !ok {
		return errors.New("unsupported notification channel")
	}

	return notify(ctx, s.NotificationContext, config, task)
}

type Map map[string]interface{}

const (
	green = "#5cb85c"
	red   = "#d9534f"
	grey  = "#808080"
)

var Slack NotificationChannel = func(ctx context.Context, nctx *NotificationContext, configValue interface{}, task LoggedTask) error {
	var config struct {
		URL string `mapstructure:"url"`
	}

	if err := mapstructure.Decode(configValue, &config); err != nil {
		return errors.Wrapf(err, "decode config: %+v", configValue)
	}

	if config.URL == "" {
		return errors.New("no slack webhook URL specified")
	}

	source := task.Source
	if dot := strings.Index(source, "."); dot >= 0 && dot < len(source)-1 {
		source = source[dot+1:]
	}

	var logs string
	if nctx.UIBaseURL != "" {
		source = fmt.Sprintf("<%s/sources/edit/%s|%s>", nctx.UIBaseURL, source, source)
		logs = fmt.Sprintf("<%s/sources/logs/%s/%s|See logs>", nctx.UIBaseURL, source, task.ID)
	} else {
		logs = "*Logs:*\n" + strings.Join(task.Collect(), "\n")
	}

	if task.Status == SUCCESS.String() {
		logs = ""
	}

	color := grey
	switch task.Status {
	case SUCCESS.String():
		color = green
	case FAILED.String():
		color = red
	}

	return requests.URL(config.URL).
		Method(http.MethodPost).
		BodyJSON(Map{
			"text": fmt.Sprintf("*%s %s* [%s]: Synchronization %s", nctx.ServiceName, nctx.Version, nctx.ServerName, task.Status),
			"attachments": []Map{{
				"color": color,
				"blocks": []Map{
					{
						"type": "divider",
					},
					{
						"type": "section",
						"text": Map{
							"type": "mrkdwn",
							"text": fmt.Sprintf("*Connector type:* %s\n*Connector:* %s\n*Collection:* %s\n%s",
								task.SourceType, source, task.Collection, logs),
						},
					},
				},
			}},
		}).
		CheckStatus(http.StatusOK).
		Fetch(ctx)
}
