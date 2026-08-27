package facts

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// ollamaMessage mirrors the OpenAI-style message shape used by Ollama's native
// /api/chat endpoint.
type ollamaMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// ollamaChatRequest is the request body for Ollama's /api/chat endpoint. Unlike
// OpenAI, JSON forcing is expressed with the `format` field and streaming must
// be disabled to receive a single response object.
type ollamaChatRequest struct {
	Model    string          `json:"model"`
	Messages []ollamaMessage `json:"messages"`
	Stream   bool            `json:"stream"`
	Format   string          `json:"format,omitempty"`
}

// ollamaChatResponse matches the non-streaming reply from /api/chat. Errors are
// surfaced either via a non-200 status or an `error` string in the body.
type ollamaChatResponse struct {
	Message struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	} `json:"message"`
	Done  bool   `json:"done"`
	Error string `json:"error"`
}

const DefaultOllamaModel = "gemma2:2b"

// OllamaAnalyzer is an Analyzer that forwards a link's readable text to a local
// or remote Ollama /api/chat endpoint and returns the model's JSON reply as the
// pass result. It honors the same DefaultAnalyzerPrompt as OpenAIAnalyzer.
type OllamaAnalyzer struct {
	APIURL string
	APIKey string
	Model  string
	Prompt string
	Client *http.Client
}

func NewOllamaAnalyzer(apiURL, apiKey string) *OllamaAnalyzer {
	return &OllamaAnalyzer{
		APIURL: apiURL,
		APIKey: apiKey,
		Model:  DefaultOllamaModel,
		Prompt: DefaultAnalyzerPrompt,
		Client: &http.Client{Timeout: 600 * time.Second},
	}
}

func (a *OllamaAnalyzer) Analyze(ctx context.Context, content string) (string, error) {
	model := a.Model
	if model == "" {
		model = DefaultOllamaModel
	}
	client := a.Client
	if client == nil {
		client = &http.Client{Timeout: 600 * time.Second}
	}

	reqBody := ollamaChatRequest{
		Model:  model,
		Stream: false,
		Format: "json",
		Messages: []ollamaMessage{
			{Role: "system", Content: a.Prompt},
			{Role: "user", Content: content},
		},
	}

	payload, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("marshal request: %w", err)
	}

	endpoint := strings.TrimRight(a.APIURL, "/") + "/api/chat"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return "", fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if a.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+a.APIKey)
	}

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

	var parsed ollamaChatResponse
	if err := json.Unmarshal(respBytes, &parsed); err != nil {
		return "", fmt.Errorf("decode response: %w", err)
	}
	if parsed.Error != "" {
		return "", fmt.Errorf("llm error: %s", parsed.Error)
	}
	if parsed.Message.Content == "" {
		return "", fmt.Errorf("llm returned empty content: %s", string(respBytes))
	}

	return stripCodeFences(parsed.Message.Content), nil
}
