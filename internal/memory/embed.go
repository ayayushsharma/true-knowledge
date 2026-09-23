// Embeddings for notes via an Ollama-compatible /api/embed endpoint. The
// endpoint is external by design (no bundled model); BM25 stays authoritative:
// any embed failure, timeout, or model mismatch degrades to BM25 search.
package memory

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"sort"
	"strings"
	"time"
)

// Embedder talks to one /api/embed endpoint.
type Embedder struct {
	Endpoint string
	Model    string
	Timeout  time.Duration
}

// NewEmbedder builds an embedder (nil when disabled/nonempty checks fail).
func NewEmbedder(endpoint, model string, timeoutMS int) *Embedder {
	if endpoint == "" || model == "" {
		return nil
	}
	if timeoutMS <= 0 {
		timeoutMS = 3000
	}
	return &Embedder{Endpoint: strings.TrimSuffix(endpoint, "/"), Model: model, Timeout: time.Duration(timeoutMS) * time.Millisecond}
}

// Embed returns one vector per input text.
func (e *Embedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, errors.New("no texts to embed")
	}
	body, _ := json.Marshal(map[string]any{"model": e.Model, "input": texts})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.Endpoint+"/api/embed", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	hc := &http.Client{Timeout: e.Timeout}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("embed endpoint HTTP %s: %s", resp.Status, firstLine(string(raw)))
	}
	var out struct {
		Embeddings [][]float32 `json:"embeddings"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("parse embed response: %w", err)
	}
	if len(out.Embeddings) != len(texts) {
		return nil, fmt.Errorf("embed returned %d vectors for %d texts", len(out.Embeddings), len(texts))
	}
	return out.Embeddings, nil
}

// EmbedOne is a convenience wrapper for a single text.
func (e *Embedder) EmbedOne(ctx context.Context, text string) ([]float32, error) {
	vecs, err := e.Embed(ctx, []string{text})
	if err != nil {
		return nil, err
	}
	return vecs[0], nil
}

// cosine returns the cosine similarity of two equal-length vectors.
func cosine(a, b []float32) float32 {
	var dot, na, nb float64
	for i := range a {
		x, y := float64(a[i]), float64(b[i])
		dot += x * y
		na += x * x
		nb += y * y
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return float32(dot / (math.Sqrt(na) * math.Sqrt(nb)))
}

// encodeVec packs float32s little-endian into a BLOB.
func encodeVec(v []float32) []byte {
	b := make([]byte, len(v)*4)
	for i, f := range v {
		binary.LittleEndian.PutUint32(b[i*4:], math.Float32bits(f))
	}
	return b
}

// decodeVec unpacks an encoded vector; ok=false on length mismatch.
func decodeVec(b []byte) ([]float32, bool) {
	if len(b)%4 != 0 {
		return nil, false
	}
	out := make([]float32, len(b)/4)
	for i := range out {
		out[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[i*4:]))
	}
	return out, true
}

// rrFuse merges ranked id lists with Reciprocal Rank Fusion into a single
// scored, ordered slice (k = 60).
func rrFuse(limit int, lists ...[]string) []string {
	sc := rrScoreMap(lists...)
	var ids []string
	for id := range sc {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		if sc[ids[i]] != sc[ids[j]] {
			return sc[ids[i]] > sc[ids[j]]
		}
		return ids[i] < ids[j]
	})
	if len(ids) > limit {
		ids = ids[:limit]
	}
	return ids
}

// rrScoreMap maps each ranked id to its Reciprocal Rank Fusion score (k = 60).
func rrScoreMap(lists ...[]string) map[string]float64 {
	const k = 60
	scores := map[string]float64{}
	for _, l := range lists {
		for i, id := range l {
			scores[id] += 1.0 / (float64(k) + float64(i+1))
		}
	}
	return scores
}

func firstLine(s string) string {
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			return s[:i]
		}
	}
	return s
}
