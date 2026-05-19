package service

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/imroc/req/v3"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type openAIImagesRoundTripFunc func(*http.Request) (*http.Response, error)

func (f openAIImagesRoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func TestOpenAIGatewayServiceParseOpenAIImagesRequest_JSON(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := []byte(`{"model":"gpt-image-2","prompt":"draw a cat","size":"1024x1024","quality":"high","stream":true}`)

	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = req

	svc := &OpenAIGatewayService{}
	parsed, err := svc.ParseOpenAIImagesRequest(c, body)
	require.NoError(t, err)
	require.NotNil(t, parsed)
	require.Equal(t, "/v1/images/generations", parsed.Endpoint)
	require.Equal(t, "gpt-image-2", parsed.Model)
	require.Equal(t, "draw a cat", parsed.Prompt)
	require.True(t, parsed.Stream)
	require.Equal(t, "1024x1024", parsed.Size)
	require.Equal(t, "1K", parsed.SizeTier)
	require.Equal(t, OpenAIImagesCapabilityNative, parsed.RequiredCapability)
	require.False(t, parsed.Multipart)
}

func TestOpenAIGatewayServiceParseOpenAIImagesRequest_MultipartEdit(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	require.NoError(t, writer.WriteField("model", "gpt-image-2"))
	require.NoError(t, writer.WriteField("prompt", "replace background"))
	require.NoError(t, writer.WriteField("size", "1536x1024"))
	part, err := writer.CreateFormFile("image", "source.png")
	require.NoError(t, err)
	_, err = part.Write([]byte("fake-image-bytes"))
	require.NoError(t, err)
	require.NoError(t, writer.Close())

	req := httptest.NewRequest(http.MethodPost, "/v1/images/edits", bytes.NewReader(body.Bytes()))
	req.Header.Set("Content-Type", writer.FormDataContentType())
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = req

	svc := &OpenAIGatewayService{}
	parsed, err := svc.ParseOpenAIImagesRequest(c, body.Bytes())
	require.NoError(t, err)
	require.NotNil(t, parsed)
	require.Equal(t, "/v1/images/edits", parsed.Endpoint)
	require.True(t, parsed.Multipart)
	require.Equal(t, "gpt-image-2", parsed.Model)
	require.Equal(t, "replace background", parsed.Prompt)
	require.Equal(t, "1536x1024", parsed.Size)
	require.Equal(t, "2K", parsed.SizeTier)
	require.Len(t, parsed.Uploads, 1)
	require.Equal(t, OpenAIImagesCapabilityNative, parsed.RequiredCapability)
}

func TestOpenAIGatewayServiceParseOpenAIImagesRequest_PromptOnlyDefaultsRemainBasic(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := []byte(`{"prompt":"draw a cat"}`)

	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = req

	svc := &OpenAIGatewayService{}
	parsed, err := svc.ParseOpenAIImagesRequest(c, body)
	require.NoError(t, err)
	require.NotNil(t, parsed)
	require.Equal(t, "gpt-image-2", parsed.Model)
	require.Equal(t, OpenAIImagesCapabilityBasic, parsed.RequiredCapability)
}

func TestOpenAIGatewayServiceParseOpenAIImagesRequest_ExplicitSizeRequiresNativeCapability(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := []byte(`{"prompt":"draw a cat","size":"1024x1024"}`)

	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = req

	svc := &OpenAIGatewayService{}
	parsed, err := svc.ParseOpenAIImagesRequest(c, body)
	require.NoError(t, err)
	require.NotNil(t, parsed)
	require.Equal(t, OpenAIImagesCapabilityNative, parsed.RequiredCapability)
}

func TestOpenAIGatewayServiceParseOpenAIImagesRequest_RejectsNonImageModel(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := []byte(`{"model":"gpt-5.4","prompt":"draw a cat"}`)

	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = req

	svc := &OpenAIGatewayService{}
	parsed, err := svc.ParseOpenAIImagesRequest(c, body)
	require.Nil(t, parsed)
	require.ErrorContains(t, err, `images endpoint requires an image model, got "gpt-5.4"`)
}

func TestResolveOpenAIImageTimeouts(t *testing.T) {
	require.Equal(t, 180*time.Second, resolveOpenAIImagePollTimeout(nil))
	require.Equal(t, 240*time.Second, resolveOpenAIImageLifecycleTimeout(nil))

	cfg := &config.Config{
		Gateway: config.GatewayConfig{
			OpenAIImagePollTimeoutSeconds:      210,
			OpenAIImageLifecycleTimeoutSeconds: 270,
		},
	}

	require.Equal(t, 210*time.Second, resolveOpenAIImagePollTimeout(cfg))
	require.Equal(t, 270*time.Second, resolveOpenAIImageLifecycleTimeout(cfg))
}

func TestPollOpenAIImageConversation_ReturnsSyntheticTimeout(t *testing.T) {
	calls := 0
	client := req.C()
	client.GetClient().Transport = openAIImagesRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		require.Equal(t, "chatgpt.com", r.URL.Host)
		require.Equal(t, "/backend-api/conversation/conv-timeout", r.URL.Path)
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"mapping":{}}`)),
			Request:    r,
		}, nil
	})

	pointers, err := pollOpenAIImageConversation(context.Background(), client, http.Header{}, "conv-timeout", 5*time.Millisecond)
	require.Nil(t, pointers)
	require.Error(t, err)

	var statusErr *openAIImageStatusError
	require.ErrorAs(t, err, &statusErr)
	require.Equal(t, http.StatusRequestTimeout, statusErr.StatusCode)
	require.Contains(t, statusErr.Message, "openai image generation timed out")
	require.GreaterOrEqual(t, calls, 1)
}

func TestRewriteOpenAIImagesModel_StripsUnsupportedResponseFormat(t *testing.T) {
	body := []byte(`{"model":"gpt-image-1","prompt":"draw a cat","response_format":"b64_json"}`)

	rewritten, contentType, err := rewriteOpenAIImagesModel(body, "application/json", "gpt-image-2")
	require.NoError(t, err)
	require.Equal(t, "application/json", contentType)
	require.Equal(t, "gpt-image-2", gjson.GetBytes(rewritten, "model").String())
	require.False(t, gjson.GetBytes(rewritten, "response_format").Exists())
}

func TestRewriteOpenAIImagesMultipartModel_StripsUnsupportedResponseFormat(t *testing.T) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	require.NoError(t, writer.WriteField("model", "gpt-image-1"))
	require.NoError(t, writer.WriteField("prompt", "draw a cat"))
	require.NoError(t, writer.WriteField("response_format", "b64_json"))
	require.NoError(t, writer.Close())

	rewritten, contentType, err := rewriteOpenAIImagesModel(body.Bytes(), writer.FormDataContentType(), "gpt-image-2")
	require.NoError(t, err)

	_, params, err := mime.ParseMediaType(contentType)
	require.NoError(t, err)
	form, err := multipart.NewReader(bytes.NewReader(rewritten), params["boundary"]).ReadForm(1 << 20)
	require.NoError(t, err)
	defer func() { _ = form.RemoveAll() }()

	require.Equal(t, []string{"gpt-image-2"}, form.Value["model"])
	require.Equal(t, []string{"draw a cat"}, form.Value["prompt"])
	require.NotContains(t, form.Value, "response_format")
}

func TestBuildOpenAIImageResponseIncludesUsage(t *testing.T) {
	body, imageCount, err := buildOpenAIImageResponse(
		context.Background(),
		nil,
		nil,
		"",
		[]openAIImagePointerInfo{{B64JSON: "QUJD"}},
		OpenAIUsage{InputTokens: 12, OutputTokens: 34},
	)
	require.NoError(t, err)
	require.Equal(t, 1, imageCount)

	var payload map[string]any
	require.NoError(t, json.Unmarshal(body, &payload))
	require.Len(t, payload["data"], 1)

	usage, ok := payload["usage"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, float64(12), usage["input_tokens"])
	require.Equal(t, float64(34), usage["output_tokens"])
	require.Equal(t, float64(46), usage["total_tokens"])

	outputDetails, ok := usage["output_tokens_details"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, float64(34), outputDetails["image_tokens"])
}

func TestCollectOpenAIImagePointers_RecognizesDirectAssets(t *testing.T) {
	items := collectOpenAIImagePointers([]byte(`{
		"revised_prompt": "cat astronaut",
		"parts": [
			{"b64_json":"QUJD"},
			{"download_url":"https://files.example.com/image.png?sig=1"},
			{"asset_pointer":"file-service://file_123"}
		]
	}`))

	require.Len(t, items, 3)
	var sawBase64, sawURL, sawPointer bool
	for _, item := range items {
		if item.B64JSON == "QUJD" {
			sawBase64 = true
			require.Equal(t, "cat astronaut", item.Prompt)
		}
		if item.DownloadURL == "https://files.example.com/image.png?sig=1" {
			sawURL = true
		}
		if item.Pointer == "file-service://file_123" {
			sawPointer = true
		}
	}
	require.True(t, sawBase64)
	require.True(t, sawURL)
	require.True(t, sawPointer)
}

func TestExtractOpenAIImageConversationState_UsesOnlyImageGenToolOutput(t *testing.T) {
	state := extractOpenAIImageConversationState([]byte(`{
		"async_status": 4,
		"mapping": {
			"user_msg": {
				"message": {
					"author": {"role": "user"},
					"status": "finished_successfully",
					"content": {
						"content_type": "multimodal_text",
						"parts": [
							{"asset_pointer": "sediment://input_image", "content_type": "image_asset_pointer"},
							"replace text"
						]
					},
					"metadata": {}
				}
			},
			"tool_msg": {
				"message": {
					"author": {"role": "tool"},
					"status": "finished_successfully",
					"create_time": 1770000000,
					"content": {
						"content_type": "multimodal_text",
						"parts": [
							{"asset_pointer": "sediment://output_image", "content_type": "image_asset_pointer"}
						]
					},
					"metadata": {
						"async_task_type": "image_gen",
						"async_task_id": "imagegen_success",
						"image_gen_title": "updated image"
					}
				}
			}
		}
	}`))

	require.Equal(t, "4", state.AsyncStatus)
	require.Equal(t, 1, state.ImageGenMessages)
	require.False(t, state.HasImageGenError())
	require.Len(t, state.OutputPointerInfo, 1)
	require.Equal(t, "sediment://output_image", state.OutputPointerInfo[0].Pointer)
	require.Equal(t, "updated image", state.OutputPointerInfo[0].Prompt)
}

func TestExtractOpenAIImageConversationState_DetectsImageGenErrorAndIgnoresInputAsset(t *testing.T) {
	state := extractOpenAIImageConversationState([]byte(`{
		"async_status": "4",
		"mapping": {
			"user_msg": {
				"message": {
					"author": {"role": "user"},
					"status": "finished_successfully",
					"content": {
						"content_type": "multimodal_text",
						"parts": [
							{"asset_pointer": "sediment://input_image", "content_type": "image_asset_pointer"},
							"edit this image"
						]
					},
					"metadata": {}
				}
			},
			"error_msg": {
				"message": {
					"author": {"role": "assistant"},
					"status": "finished_successfully",
					"create_time": 1770000010,
					"content": {
						"content_type": "text",
						"parts": ["生成的图片可能违反了防护限制，请修改提示语。"]
					},
					"metadata": {
						"async_task_type": "image_gen",
						"async_task_id": "imagegen_failed",
						"is_error": true
					}
				}
			}
		}
	}`))

	require.Equal(t, "4", state.AsyncStatus)
	require.Equal(t, 1, state.ImageGenMessages)
	require.True(t, state.HasImageGenError())
	require.Equal(t, "imagegen_failed", state.ErrorTaskID)
	require.Contains(t, state.ErrorReason, "防护限制")
	require.Empty(t, state.OutputPointerInfo)
}

func TestResolveOpenAIImageBytes_PrefersInlineBase64(t *testing.T) {
	data, err := resolveOpenAIImageBytes(context.Background(), nil, nil, "", openAIImagePointerInfo{
		B64JSON: "data:image/png;base64,QUJD",
	})
	require.NoError(t, err)
	require.Equal(t, []byte("ABC"), data)
}
