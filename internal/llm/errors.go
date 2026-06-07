package llm

import "errors"

// ErrNoEmbedder is returned by NoopEmbedder.Embed.
var ErrNoEmbedder = errors.New("llm: no embedder configured")

// ErrNoClient is returned by NoopClient.Generate.
var ErrNoClient = errors.New("llm: no chat client configured")
