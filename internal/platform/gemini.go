package platform

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"

	"google.golang.org/genai"
)

const (
	defaultGeminiModel = "gemini-2.5-flash"
)

const systemPrompt = `You are the central Diagnostics, Observability, and Security engine of AutoHost.
Your goal is to analyze pre-filtered log streams from servers, Docker containers, microservices, databases, and network traffic to generate accurate, actionable, and structured technical diagnostics in English.

### Analysis Guidelines:
1. Technical Precision: Identify the true root cause without assuming information not present in the logs. Write all summaries, root causes, findings, patterns, and suggested actions in English.
2. Multi-Failure Detection: If there are multiple independent errors or cascading failed dependencies, break down each one as an individual item in "issues".
3. Security Focus: Analyze malicious patterns (SSH/Auth brute force, injection, default credentials, exposed tokens, unprotected sensitive ports) and record them in "security_insights".
4. Patterns & Anomalies: Detect reconnection loops (crash-loops), latency degradation, memory leaks, or resource saturation in "detected_patterns".
5. Deterministic Actions: In "suggested_fix", provide the exact bash/docker command or configuration directive needed to resolve the issue with minimal friction.
6. Nominal State: If the provided logs contain no real anomalies or errors, set "system_health": "HEALTHY" and leave "issues" and "security_insights" arrays empty.

### Mandatory Output Format:
You MUST respond with ONLY a valid JSON object matching this schema:
{
  "summary": "string",
  "system_health": "HEALTHY" | "DEGRADED" | "CRITICAL",
  "issues": [
    {
      "id": "err-1",
      "type": "RUNTIME_ERROR" | "CONFIG_ERROR" | "NETWORK_TIMEOUT" | "DATABASE_FAILURE" | "RESOURCE_LIMIT" | "UNKNOWN",
      "severity": "LOW" | "MEDIUM" | "CRITICAL",
      "component": "string",
      "root_cause": "string",
      "suggested_fix": {
        "action": "string",
        "command": "string or null"
      }
    }
  ],
  "security_insights": [
    {
      "finding": "string",
      "risk_level": "INFO" | "WARNING" | "HIGH",
      "recommendation": "string"
    }
  ],
  "detected_patterns": [
    "string"
  ]
}`

type GeminiClient struct {
	client *genai.Client
	model  string
}

func NewGeminiClient() *GeminiClient {
	ctx := context.Background()

	projectID := strings.TrimSpace(os.Getenv("GCP_PROJECT_ID"))
	if projectID == "" {
		projectID = strings.TrimSpace(os.Getenv("GOOGLE_CLOUD_PROJECT"))
	}

	location := strings.TrimSpace(os.Getenv("GCP_LOCATION"))
	if location == "" {
		location = strings.TrimSpace(os.Getenv("GOOGLE_CLOUD_LOCATION"))
	}
	if location == "" {
		location = "us-central1"
	}

	apiKey := strings.TrimSpace(os.Getenv("GEMINI_API_KEY"))
	if apiKey == "" {
		apiKey = strings.TrimSpace(os.Getenv("GOOGLE_API_KEY"))
	}

	model := strings.TrimSpace(os.Getenv("GEMINI_MODEL"))
	if model == "" {
		model = defaultGeminiModel
	}

	var clientConfig *genai.ClientConfig
	if projectID != "" || os.Getenv("GOOGLE_GENAI_USE_VERTEXAI") == "true" || os.Getenv("GOOGLE_GENAI_USE_VERTEXAI") == "1" {
		// Vertex AI Backend (directamente a cuenta GCP / créditos de facturación)
		clientConfig = &genai.ClientConfig{
			Project:  projectID,
			Location: location,
			Backend:  genai.BackendVertexAI,
		}
		if apiKey != "" {
			clientConfig.APIKey = apiKey
		}
	} else if apiKey != "" {
		// Google AI Studio / Gemini Developer API Backend
		clientConfig = &genai.ClientConfig{
			APIKey:  apiKey,
			Backend: genai.BackendGeminiAPI,
		}
	} else {
		return &GeminiClient{model: model}
	}

	client, err := genai.NewClient(ctx, clientConfig)
	if err != nil {
		log.Printf("[WARN] Failed to initialize GenAI client: %v", err)
		return &GeminiClient{model: model}
	}

	return &GeminiClient{
		client: client,
		model:  model,
	}
}

func (g *GeminiClient) IsConfigured() bool {
	return g != nil && g.client != nil
}

// AnalyzeLogs sends sanitized logs to Gemini to generate/enrich the diagnostic report.
func (g *GeminiClient) AnalyzeLogs(ctx context.Context, logsText string) (string, error) {
	if !g.IsConfigured() {
		return "", fmt.Errorf("Gemini client is not configured (set GCP_PROJECT_ID or GEMINI_API_KEY)")
	}

	prompt := "Analyze the following sanitized log streams and generate the technical diagnostic report in English:\n\n" + logsText

	temp := float32(0.2)
	config := &genai.GenerateContentConfig{
		SystemInstruction: &genai.Content{
			Parts: []*genai.Part{
				{Text: systemPrompt},
			},
		},
		ResponseMIMEType: "application/json",
		Temperature:      &temp,
	}

	resp, err := g.client.Models.GenerateContent(ctx, g.model, genai.Text(prompt), config)
	if err != nil {
		return "", fmt.Errorf("Gemini generate content failed: %w", err)
	}

	text := strings.TrimSpace(resp.Text())
	if text == "" {
		return "", fmt.Errorf("empty response from Gemini")
	}

	// Basic validation that output is valid JSON
	var testJSON map[string]interface{}
	if err := json.Unmarshal([]byte(text), &testJSON); err != nil {
		log.Printf("[WARN] Gemini output is not valid JSON: %s", text)
		return "", fmt.Errorf("Gemini output is not valid JSON: %w", err)
	}

	return text, nil
}

