package insights

import (
	"context"

	"github.com/openai/openai-go/v3"
)

// Dimensions is how wide a passage vector is.
//
// text-embedding-3 lets the size be asked for, and 512 is a deliberate trade:
// a third of the storage of the full 1536 and, on OpenAI's own MTEB numbers,
// within about a point of it. A meeting a day for a year is roughly a hundred
// thousand passages, which at 512 float32s is 200 MB of database — large, but
// a laptop can hold it and search it in memory.
const Dimensions = 512

// Embedder is the model. Cheap enough that indexing a meeting costs a fraction
// of a cent, which is why the whole transcript is indexed rather than a summary.
const Embedder = openai.EmbeddingModelTextEmbedding3Small

// Embed turns passages into vectors. A batch, because the round trip dominates
// and a meeting is a few hundred passages.
func (c *Client) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if !c.Ready() {
		return nil, ErrNoKey
	}
	if len(texts) == 0 {
		return nil, nil
	}
	resp, err := c.api.Embeddings.New(ctx, openai.EmbeddingNewParams{
		Model:      Embedder,
		Dimensions: openai.Int(Dimensions),
		Input:      openai.EmbeddingNewParamsInputUnion{OfArrayOfStrings: texts},
	})
	if err != nil {
		return nil, err
	}
	// Returned in order, but indexed anyway: a caller lining vectors up against
	// its own passages by position would be silently wrong if that ever changed.
	out := make([][]float32, len(texts))
	for _, item := range resp.Data {
		if int(item.Index) >= len(out) {
			continue
		}
		v := make([]float32, len(item.Embedding))
		for i, x := range item.Embedding {
			v[i] = float32(x)
		}
		out[item.Index] = v
	}
	return out, nil
}
