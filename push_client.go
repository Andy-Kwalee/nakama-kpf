// Copyright 2024 Kwalee Limited.
// All rights reserved.
//
// NOTICE: All information contained herein is, and remains the property of Kwalee Limited. and
// its suppliers, if any. The intellectual and technical concepts contained herein are proprietary
// to Kwalee Limited and its suppliers and may be covered by U.S. and Foreign Patents, patents in
// process, and are protected by trade secret or copyright law. Dissemination of this information
// or reproduction of this material is strictly forbidden unless prior written permission is
// obtained from Kwalee Limited.

package kpf

import (
	"context"
	"database/sql"
	"errors"

	"github.com/heroiclabs/nakama-common/runtime"

	firebase "firebase.google.com/go/v4"
	"firebase.google.com/go/v4/messaging"
	"github.com/sideshow/apns2"
	"github.com/sideshow/apns2/payload"
	"github.com/sideshow/apns2/token"
	"google.golang.org/api/option"

	// Pin transitive Go dependencies for binary compatibility.
	_ "golang.org/x/net/http2"
	_ "golang.org/x/oauth2"
	_ "golang.org/x/text"
	_ "google.golang.org/genproto/googleapis/api"
	_ "google.golang.org/genproto/googleapis/rpc/status"
)

type APNSCredentials struct {
	Environment       string
	TeamID            string
	Topic             string // The application's bundle ID / App ID.
	P8AuthKeyID       string
	P8AuthKeyFilePath string
}

func NewAPNSCredentialsFromEnv(env map[string]string) *APNSCredentials {
	creds := &APNSCredentials{
		Environment:       env["APNS_ENVIRONMENT"],
		TeamID:            env["APNS_TEAM_ID"],
		Topic:             env["APNS_TOPIC"],
		P8AuthKeyID:       env["APNS_P8_AUTH_KEY_ID"],
		P8AuthKeyFilePath: env["APNS_P8_AUTH_KEY_FILE_PATH"],
	}
	return creds
}

type FCMCredentials struct {
	ProjectID           string
	CredentialsFilePath string // Parsed in JSON format.
}

func NewFCMCredentialsFromEnv(env map[string]string) *FCMCredentials {
	creds := &FCMCredentials{
		ProjectID:           env["FCM_PROJECT_ID"],
		CredentialsFilePath: env["FCM_CREDENTIALS_FILE_PATH"],
	}
	return creds
}

type PushClient struct {
	apnsClient *apns2.Client
	apnsCreds  *APNSCredentials
	ctx        context.Context
	fcmClient  *messaging.Client
	fcmCreds   *FCMCredentials
}

type PushNotificationDeliveryFailed struct {
	Err error
}

func (e *PushNotificationDeliveryFailed) Error() string {
	return e.Err.Error()
}

func NewPushClient(ctx context.Context, apnsCreds *APNSCredentials, fcmCreds *FCMCredentials) *PushClient {
	client := &PushClient{
		apnsCreds: apnsCreds,
		ctx:       ctx,
		fcmCreds:  fcmCreds,

		// Lazy init.
		apnsClient: nil,
		fcmClient:  nil,
	}
	return client
}

func NewPushClientFromEnv(ctx context.Context, props map[string]string) *PushClient {
	apnsCreds := NewAPNSCredentialsFromEnv(props)
	fcmCreds := NewFCMCredentialsFromEnv(props)
	return NewPushClient(ctx, apnsCreds, fcmCreds)
}

func (c *PushClient) getAPNSClient() (*apns2.Client, error) {
	if c.apnsClient == nil {
		authKey, err := token.AuthKeyFromFile(c.apnsCreds.P8AuthKeyFilePath)
		if err != nil {
			return nil, err
		}
		tok := &token.Token{
			AuthKey: authKey,
			KeyID:   c.apnsCreds.P8AuthKeyID,
			TeamID:  c.apnsCreds.TeamID,
		}
		apnsClient := apns2.NewTokenClient(tok).Production()
		if c.apnsCreds.Environment == "development" {
			apnsClient = apnsClient.Development()
		}
		c.apnsClient = apnsClient
	}
	return c.apnsClient, nil
}

func (c *PushClient) getFCMClient() (*messaging.Client, error) {
	if c.fcmClient == nil {
		opts := option.WithCredentialsFile(c.fcmCreds.CredentialsFilePath)
		fcmConfig := &firebase.Config{
			ProjectID: c.fcmCreds.ProjectID,
		}
		app, err := firebase.NewApp(c.ctx, fcmConfig, opts)
		if err != nil {
			return nil, err
		}
		fcmClient, err := app.Messaging(c.ctx)
		if err != nil {
			return nil, err
		}
		c.fcmClient = fcmClient
	}
	return c.fcmClient, nil
}

func (c *PushClient) SendUserPush(ctx context.Context, logger runtime.Logger, db *sql.DB, userID, title, body, imageURL, externalID string) error {
	query := `
SELECT
    id, push_token_android, push_token_ios
FROM
    user_device
WHERE
    user_id = $1
`
	rows, err := db.QueryContext(ctx, query, userID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return errors.New("user not found")
		}
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var (
			id               string
			pushTokenAndroid sql.NullString
			pushTokenIos     sql.NullString
		)
		if err := rows.Scan(&id, &pushTokenAndroid, &pushTokenIos); err != nil {
			return err
		}
		if pushTokenAndroid.Valid && pushTokenAndroid.String != "" {
			if err := c.SendFirebasePush(ctx, pushTokenAndroid.String, title, body, imageURL, externalID); err != nil {
				return err
			}
		}
		if pushTokenIos.Valid && pushTokenIos.String != "" {
			if err := c.SendApplePush(ctx, pushTokenIos.String, title, body, imageURL, externalID); err != nil {
				return err
			}
		}
	}
	return nil
}

func (c *PushClient) SendApplePush(ctx context.Context, deviceToken, title, body, imageURL, externalID string) error {
	contents := payload.NewPayload().AlertTitle(title).AlertBody(body)
	contents = contents.Custom("image_url", imageURL).Custom("external_id", externalID)
	notification := &apns2.Notification{
		DeviceToken: deviceToken,
		Topic:       c.apnsCreds.Topic,
		Payload:     contents,
	}

	apnsClient, err := c.getAPNSClient()
	if err != nil {
		return err
	}
	response, err := apnsClient.PushWithContext(ctx, notification)
	if err != nil {
		return &PushNotificationDeliveryFailed{Err: err}
	}
	if !response.Sent() {
		return &PushNotificationDeliveryFailed{Err: errors.New(response.Reason)}
	}
	return nil
}

func (c *PushClient) SendFirebasePush(ctx context.Context, deviceToken, title, body, imageURL, externalID string) error {
	contents := &messaging.Message{
		Notification: &messaging.Notification{
			Title:    title,
			Body:     body,
			ImageURL: imageURL,
		},
		Token: deviceToken,
		Data: map[string]string{
			"external_id": externalID,
		},
	}

	fcmClient, err := c.getFCMClient()
	if err != nil {
		return err
	}
	if _, err := fcmClient.Send(ctx, contents); err != nil {
		return &PushNotificationDeliveryFailed{Err: err}
	}
	return nil
}
