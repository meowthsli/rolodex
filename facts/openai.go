package facts

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type oaMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type openAIChatRequest struct {
	Model          string      `json:"model"`
	Messages       []oaMessage `json:"messages"`
	ResponseFormat struct {
		Type string `json:"type"`
	} `json:"response_format"`
}

type openAIChatResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

const DefaultOpenAIModel = "qwen3.6"

// OpenAIAnalyzer is an Analyzer that forwards a link's readable text to an
// external OpenAI-compatible chat completions endpoint and returns the model's
// JSON reply as the pass result.
type OpenAIAnalyzer struct {
	APIURL string
	APIKey string
	Model  string
	Client *http.Client
}

func NewOpenAIAnalyzer(apiURL, apiKey string) *OpenAIAnalyzer {
	return &OpenAIAnalyzer{
		APIURL: apiURL,
		APIKey: apiKey,
		Model:  DefaultOpenAIModel,
		Client: &http.Client{Timeout: 300 * time.Second},
	}
}

func (a *OpenAIAnalyzer) Analyze(ctx context.Context, prompt, content string) (string, error) {
	model := a.Model
	if model == "" {
		model = DefaultOpenAIModel
	}
	if prompt == "" {
		return "", errors.New("Prompt is empty")
	}
	client := a.Client
	if client == nil {
		client = &http.Client{Timeout: 300 * time.Second}
	}

	reqBody := openAIChatRequest{
		Model: model,
		Messages: []oaMessage{
			{Role: "system", Content: prompt},
			{Role: "user", Content: content},
		},
	}
	reqBody.ResponseFormat.Type = "json_object"

	payload, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("marshal request: %w", err)
	}

	endpoint := strings.TrimRight(a.APIURL, "/") + "/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return "", fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+a.APIKey)

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("llm request: %w", err)
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("llm returned status %d: %s", resp.StatusCode, string(respBytes))
	}

	var parsed openAIChatResponse
	if err := json.Unmarshal(respBytes, &parsed); err != nil {
		return "", fmt.Errorf("decode response: %w", err)
	}
	if parsed.Error != nil && parsed.Error.Message != "" {
		return "", fmt.Errorf("llm error: %s", parsed.Error.Message)
	}
	if len(parsed.Choices) == 0 {
		return "", fmt.Errorf("llm returned no choices: %s", string(respBytes))
	}

	return stripCodeFences(parsed.Choices[0].Message.Content), nil
}

func stripCodeFences(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "```") {
		if i := strings.Index(s, "\n"); i >= 0 {
			s = s[i+1:]
		}
		if j := strings.LastIndex(s, "```"); j >= 0 {
			s = s[:j]
		}
		s = strings.TrimSpace(s)
	}
	return s
}
