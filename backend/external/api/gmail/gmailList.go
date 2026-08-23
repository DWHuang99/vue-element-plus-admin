package gmailapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"
	"time"
)

const gmailMessagesEndpoint = "https://gmail.googleapis.com/gmail/v1/users/me/messages"

const gmailDetailConcurrency = 5

var gmailHTTPClient = &http.Client{Timeout: 15 * time.Second}

type MessageRef struct {
	ID       string `json:"id"`
	ThreadID string `json:"threadId"`
}

type MessageListResponse struct {
	Messages           []MessageRef `json:"messages"`
	NextPageToken      string       `json:"nextPageToken"`
	ResultSizeEstimate int64        `json:"resultSizeEstimate"`
}

type UnreadMessagesResponse struct {
	Messages           []MessageDetailsResponse `json:"messages"`
	NextPageToken      string                   `json:"nextPageToken"`
	ResultSizeEstimate int64                    `json:"resultSizeEstimate"`
}

type GmailAPIError struct {
	StatusCode int
	Body       string
}

func (e *GmailAPIError) Error() string {
	return fmt.Sprintf("Gmail API error: status=%d, body=%s", e.StatusCode, e.Body)
}

func ListUnreadMessages(
	ctx context.Context,
	googleAccessToken string,
) (*MessageListResponse, error) {
	return listUnreadMessages(ctx, gmailHTTPClient, gmailMessagesEndpoint, googleAccessToken)
}

func ListUnreadMessageDetails(
	ctx context.Context,
	googleAccessToken string,
) (*UnreadMessagesResponse, error) {
	return listUnreadMessageDetails(
		ctx,
		gmailHTTPClient,
		gmailMessagesEndpoint,
		googleAccessToken,
	)
}

func listUnreadMessageDetails(
	ctx context.Context,
	client *http.Client,
	messagesEndpoint string,
	googleAccessToken string,
) (*UnreadMessagesResponse, error) {
	list, err := listUnreadMessages(ctx, client, messagesEndpoint, googleAccessToken)
	if err != nil {
		return nil, err
	}

	result := &UnreadMessagesResponse{
		Messages:           make([]MessageDetailsResponse, len(list.Messages)),
		NextPageToken:      list.NextPageToken,
		ResultSizeEstimate: list.ResultSizeEstimate,
	}
	groupContext, cancel := context.WithCancel(ctx)
	defer cancel()
	semaphore := make(chan struct{}, gmailDetailConcurrency)
	var (
		waitGroup  sync.WaitGroup
		errorOnce  sync.Once
		firstError error
	)
	for index, message := range list.Messages {
		index, message := index, message
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			select {
			case semaphore <- struct{}{}:
				defer func() { <-semaphore }()
			case <-groupContext.Done():
				return
			}
			details, err := messageDetails(
				groupContext,
				client,
				messagesEndpoint,
				googleAccessToken,
				message.ID,
			)
			if err != nil {
				errorOnce.Do(func() {
					firstError = fmt.Errorf("get Gmail message %q: %w", message.ID, err)
					cancel()
				})
				return
			}
			result.Messages[index] = *details
		}()
	}
	waitGroup.Wait()
	if firstError != nil {
		return nil, firstError
	}
	return result, nil
}

func listUnreadMessages(
	ctx context.Context,
	client *http.Client,
	messagesEndpoint string,
	googleAccessToken string,
) (*MessageListResponse, error) {
	endpoint, err := url.Parse(messagesEndpoint)
	if err != nil {
		return nil, fmt.Errorf("parse Gmail URL: %w", err)
	}

	query := endpoint.Query()
	query.Set("labelIds", "INBOX")
	query.Set("q", "is:unread")
	query.Set("maxResults", "10")
	endpoint.RawQuery = query.Encode()

	var result MessageListResponse
	if err := gmailGET(ctx, client, endpoint.String(), googleAccessToken, &result); err != nil {
		return nil, err
	}
	if result.Messages == nil {
		result.Messages = []MessageRef{}
	}
	return &result, nil
}

func gmailGET(
	ctx context.Context,
	client *http.Client,
	requestURL string,
	googleAccessToken string,
	target any,
) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return fmt.Errorf("create Gmail request: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+googleAccessToken)
	request.Header.Set("Accept", "application/json")

	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("call Gmail API: %w", err)
	}
	defer response.Body.Close()

	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		body, _ := io.ReadAll(response.Body)
		return &GmailAPIError{StatusCode: response.StatusCode, Body: string(body)}
	}
	if err := json.NewDecoder(response.Body).Decode(target); err != nil {
		return fmt.Errorf("decode Gmail response: %w", err)
	}
	return nil
}
