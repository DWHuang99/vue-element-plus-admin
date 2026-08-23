package gmailapi

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
)

type MessageDetailsResponse struct {
	ID           string      `json:"id"`
	ThreadID     string      `json:"threadId"`
	LabelIDs     []string    `json:"labelIds"`
	Snippet      string      `json:"snippet"`
	HistoryID    string      `json:"historyId"`
	InternalDate string      `json:"internalDate"`
	SizeEstimate int64       `json:"sizeEstimate"`
	Payload      MessagePart `json:"payload"`
}

type MessagePart struct {
	PartID   string        `json:"partId"`
	MimeType string        `json:"mimeType"`
	Filename string        `json:"filename"`
	Headers  []HeaderRef   `json:"headers"`
	Body     BodyRef       `json:"body"`
	Parts    []MessagePart `json:"parts"`
}

type HeaderRef struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type BodyRef struct {
	AttachmentID string `json:"attachmentId"`
	Size         int64  `json:"size"`
	Data         string `json:"data"`
}

func MessageDetails(
	ctx context.Context,
	googleAccessToken string,
	messageID string,
) (*MessageDetailsResponse, error) {
	return messageDetails(
		ctx,
		gmailHTTPClient,
		gmailMessagesEndpoint,
		googleAccessToken,
		messageID,
	)
}

func messageDetails(
	ctx context.Context,
	client *http.Client,
	messagesEndpoint string,
	googleAccessToken string,
	messageID string,
) (*MessageDetailsResponse, error) {
	requestURL := fmt.Sprintf(
		"%s/%s?format=full",
		messagesEndpoint,
		url.PathEscape(messageID),
	)
	var result MessageDetailsResponse
	if err := gmailGET(ctx, client, requestURL, googleAccessToken, &result); err != nil {
		return nil, err
	}
	return &result, nil
}
