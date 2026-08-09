# 0002: Add provider-neutral file content

## Status

Proposed

## Context

Droids' neutral message vocabulary supports text, thinking, images, and tool calls, but it cannot represent a named document attached to a user message. Applications can mention files in text and expose retrieval tools, but that does not give providers native access to PDF text, page images, document extraction, or other file-input capabilities.

The abstraction must remain independent of application storage and provider-specific file lifecycle APIs. Applications may persist files in object storage and identify them with their own records, while providers may offer proprietary upload APIs and file IDs. Neither belongs in the durable, provider-neutral message vocabulary.

A file is a transport container rather than a model modality. An attached image is both a file and visual media, while PDFs, documents, presentations, spreadsheets, and text files have different provider processing behavior.

Providers do not expose a reliable, portable way to discover which attachment media types a model accepts. Model-list endpoints generally expose identifiers and coarse metadata rather than an authoritative MIME-type matrix, and compatibility gateways may differ from the upstream provider.

## Decision

Add `FileContent` as a sealed `Content` variant for named user-provided files:

```go
type FileContent struct {
    Filename  string
    MediaType string
    // URL is a fully qualified HTTPS URL or a data URL.
    URL string
}

func (FileContent) isContent() {}
```

Provide constructors for the two supported source forms:

```go
func NewFileURL(filename, mediaType, rawURL string) (FileContent, error)
func NewFileData(filename, mediaType string, data []byte) FileContent
```

`NewFileURL` validates an absolute HTTPS or data URL and rejects URL user credentials. `NewFileData` encodes the bytes as a base64 data URL using the declared media type.

Use the same source convention for unnamed image blocks:

```go
type ImageContent struct {
    MediaType string
    // URL is a fully qualified HTTPS URL or a data URL.
    URL string
}
```

Provide corresponding URL and data constructors for images. `ImageContent` remains the semantic representation for provider- or tool-produced visual blocks; `FileContent` represents a named attachment and may itself contain image media.

Droids will not represent application file references, storage keys, provider file IDs, or provider upload lifecycle state. Applications resolve and authorize their durable attachment records before constructing droids messages. Applications using expiring signed URLs regenerate them whenever they rebuild a transcript.

Providers translate `FileContent` according to media type and their capabilities. Image media may map to a native image input while documents may map to a native file input. Unsupported media types or source forms produce explicit stream errors; providers must never silently discard file content.

Do not add `file` as a model input modality alongside `image`, and do not maintain a static attachment MIME-type capability matrix in the neutral model metadata. Providers validate media support when translating a request and remain the final authority. Droids may perform structural validation, but unsupported media types or source forms are provider errors rather than a capability-discovery concern.

Droids will not integrate with provider file-upload APIs or expose provider-specific file IDs.

## Consequences

- Applications can supply PDFs, documents, spreadsheets, text files, and named images without encoding provider-specific request types into the agent loop.
- Application storage, authorization, retention, and signed URL generation remain outside droids.
- Transcripts remain portable and do not depend on the lifecycle of an OpenAI file ID.
- HTTPS sources avoid base64 expansion and loading whole files into application memory, but the provider must be able to fetch the URL before it expires.
- Data URLs support tests, local development, and small inline files at the cost of larger requests and memory use.
- Callers are responsible for persisting durable attachment references separately from expiring model-facing URLs and hydrating them before a run.
- Provider and model support differs by media type, and there is no portable preflight discovery mechanism, so callers must handle explicit unsupported-content errors.
- Changing `ImageContent` from raw base64 data to URL/data-URL sources is a breaking public API change, but aligns image and file handling while the library is still young.
- Native file inputs may consume substantial context, especially PDFs that include extracted text and rendered page images.

## Related

- [`0001-record-architecture-decisions.md`](./0001-record-architecture-decisions.md)
- [`../../VISION.md`](../../VISION.md)
- [`../../message.go`](../../message.go)
- [`../../provider_openai.go`](../../provider_openai.go)
- [OpenAI file inputs](https://developers.openai.com/api/docs/guides/file-inputs)
- [OpenAI image inputs](https://developers.openai.com/api/docs/guides/images-vision#giving-a-model-images-as-input)
