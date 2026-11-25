package providers

import (
	"encoding/json"
	"io"
	"net/http"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/packages/ssestream"
	"github.com/openai/openai-go/v3/responses"
)

// 已录制的 SSE 和非流式 fixture 走与真实 SDK service 相同的解码器。
func fixtureStream[T any](status int, body io.Reader) *ssestream.Stream[T] {
	resp := &http.Response{StatusCode: status, Body: io.NopCloser(body)}
	return ssestream.NewStream[T](ssestream.NewDecoder(resp), nil)
}

func readStream(status int, body io.Reader, emit func(Delta)) (Result, error) {
	return collectChat(fixtureStream[openai.ChatCompletionChunk](status, body), emit)
}

func readResponseStream(status int, body io.Reader, emit func(Delta)) (Result, error) {
	return collectResponse(VendorOpenAI, fixtureStream[responses.ResponseStreamEventUnion](status, body), emit)
}

func completeResponse(_ int, data []byte) (Result, error) {
	var reply responses.Response
	if err := json.Unmarshal(data, &reply); err != nil {
		return Result{}, err
	}
	return responseResult(VendorOpenAI, &reply)
}
