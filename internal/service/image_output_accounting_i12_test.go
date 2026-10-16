package service

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestOpenAIImageOutputCounterDataArrayRequiresActualImageOutput(t *testing.T) {
	nonImageBody := []byte("{\"data\":[{\"id\":\"not-an-image\",\"size\":\"4096x4096\"}]}")
	require.Zero(t, countOpenAIResponseImageOutputsFromJSONBytes(nonImageBody))
	require.Nil(t, collectOpenAIResponseImageOutputSizesFromJSONBytes(nonImageBody))

	imageBody := []byte("{\"data\":[{\"url\":\"https://example.com/image.png\",\"size\":\"2048x1152\"},{\"b64_json\":\"aW1hZ2U=\",\"size\":\"1024x1024\"}]}")
	require.Equal(t, 2, countOpenAIResponseImageOutputsFromJSONBytes(imageBody))
	require.Equal(t, []string{"2048x1152", "1024x1024"}, collectOpenAIResponseImageOutputSizesFromJSONBytes(imageBody))
}

func TestOpenAIImageOutputCounterSSEDataArrayIgnoresMetadataObjects(t *testing.T) {
	body := "data: {\"type\":\"response.completed\",\"response\":{\"output\":[]},\"data\":[{\"id\":\"metadata-only\",\"size\":\"3840x2160\"}]}\n\n" +
		"data: [DONE]\n\n"

	require.Zero(t, countOpenAIImageOutputsFromSSEBody(body))
	require.Nil(t, collectOpenAIImageOutputSizesFromSSEBody(body))
}

func TestOpenAIImageOutputCounterBackfillsNativeImagesDataSizes(t *testing.T) {
	encoded := encodeOpenAIImageTestPNG(t, 37, 23)
	body := []byte(fmt.Sprintf(`{"data":[{"b64_json":%q},{"b64_json":"not-image-data"},{"url":"https://example.com/image.png"},{"b64_json":%q,"size":"1024x1024"}]}`, encoded, encoded))

	reconciled := reconcileOpenAIResponseImageOutputSizes(body)
	require.Equal(t, "37x23", gjson.GetBytes(reconciled, "data.0.size").String())
	require.False(t, gjson.GetBytes(reconciled, "data.1.size").Exists())
	require.False(t, gjson.GetBytes(reconciled, "data.2.size").Exists())
	require.Equal(t, "1024x1024", gjson.GetBytes(reconciled, "data.3.size").String())
	require.Equal(t, 4, countOpenAIResponseImageOutputsFromJSONBytes(reconciled))
	require.Equal(t, []string{"37x23", "1024x1024"}, collectOpenAIResponseImageOutputSizesFromJSONBytes(reconciled))
}

func TestOpenAIImageOutputCounterBackfillsOutputItemResultSize(t *testing.T) {
	encoded := encodeOpenAIImageTestJPEG(t, 64, 36)
	body := []byte(fmt.Sprintf(`{"output":[{"id":"ig_valid","type":"image_generation_call","result":%q},{"id":"ig_invalid","type":"image_generation_call","result":"not-image-data"},{"id":"ig_url","type":"image_generation_call","result":"https://example.com/image.jpg"}]}`, encoded))

	require.Equal(t, 3, countOpenAIResponseImageOutputsFromJSONBytes(body))
	require.Equal(t, []string{"64x36"}, collectOpenAIResponseImageOutputSizesFromJSONBytes(body))
}
