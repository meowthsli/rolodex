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

const DefaultAnalyzerPrompt = `Вы — эксперт-аналитик в сфере венчурных инвестиций, стартапов, корпоративного управления и финансирования.
Ваша задача — проанализировать текст и извлечь из него граф знаний о домене в формате JSON.

Ограничения по домену:
Ищите только сущности типов: Startup (Компания, Организация, Интернет-портал, ООО, АО), Investor (Фонд, Ангел), Person (Основатель, CEO, Президент, Директор), Product (Продукт/Технология), Date (Дата создания, основания)
Ищите только связи типов:  ESTABLISHED_IN (Основан), INVESTED_IN (Инвестировал в), FOUNDED/COFOUNDED (Основал, Соосновал),
EMPLOYED_AT (Работает в, Назначен руководителем, Вошел в состав совета, комитета и так далее), VALUED (Оценивается в сумму),
ACQUIRED (Приобрел), SOLD (Продал всё или только долю), LAUNCHED (Запустил, участвовал в запуске, выпустил акции),
SEEDED (Привлек, получил инвестиции), SUEING (Судебные процессы), DIED (Умер), CLOSED (Закрылся, распущен, упразднен).

ПРАВИЛО АННОТИРОВАНИЯ ИСТОЧНИКА (КРИТИЧЕСКИ ВАЖНО):
Для каждой связи (relation) вы ОБЯЗАНЫ заполнить атрибуты:
1.1 "source": идентификатор сущности действующей
1.2 "target": идентификатор сущности участвующей
1.3 "type": тип связи - из известных типов.
2.1 "props.exact_quote": Вырежьте точную, дословную цитату из текста, на основе которой вы сделали вывод об этой связи, но без окружающих деталей. Не перефразируйте текст.
2.2 "props.details": обстоятельства - место, условия, интересный факт, при котором актуальна эта связь, раунд для инвестиций.
2.3 "props.amount": сумма сделки, зарплаты, компенсации или любое другое упоминание денег, если известна - в рублях, долларах; если нет, проставляем "~" (тильду).
2.4 "props.when": дата при наличии, точная или примерная до года, когда это происходило.
2.5 "props.conf": "exact" если подтверждено, "probable" если только планирует.

Уникальный ID КАПСОМ генерируется из её имени транслитераций и заменой пробелов на underscore; cлова в имени из нескольких слов упорядочить по алфавиту - "Евгений Горин" превращается в "GORIN_EVGENIY" .
Если связь вовлекает больше, чем 1 сущность (коллективное действие), продублировать эту связь в результатах, по одной на каждую участвующую сущность.

Формат ответа:
Выдайте ОБЯЗАТЕЛЬНО ТОЛЬКО валидный JSON без разметки markdown (без ` + "`" + `` + "`" + `json).
Структура JSON:
{
  "entities": [{"id": "Уникальный_ID_КАПСОМ", "type": "Тип", "properties": {"name": "Имя"}}],
  "relations": [{"source": "ID_субъекта", "target": "ID_объекта", "type": "ТИП_СВЯЗИ", "properties": {"details": "...", "exact_quote": "...", "amount": "...", "when": "...", "conf": "..."}}]
}

Текст для анализа ниже:`

// OpenAIAnalyzer is an Analyzer that forwards a link's readable text to an
// external OpenAI-compatible chat completions endpoint and returns the model's
// JSON reply as the pass result.
type OpenAIAnalyzer struct {
	APIURL string
	APIKey string
	Model  string
	Prompt string
	Client *http.Client
}

func NewOpenAIAnalyzer(apiURL, apiKey string) *OpenAIAnalyzer {
	return &OpenAIAnalyzer{
		APIURL: apiURL,
		APIKey: apiKey,
		Model:  DefaultOpenAIModel,
		Prompt: DefaultAnalyzerPrompt,
		Client: &http.Client{Timeout: 300 * time.Second},
	}
}

func (a *OpenAIAnalyzer) Analyze(ctx context.Context, content string) (string, error) {
	model := a.Model
	if model == "" {
		model = DefaultOpenAIModel
	}
	client := a.Client
	if client == nil {
		client = &http.Client{Timeout: 300 * time.Second}
	}

	reqBody := openAIChatRequest{
		Model: model,
		Messages: []oaMessage{
			{Role: "system", Content: a.Prompt},
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
