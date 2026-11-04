# paperless-gpt, a small personal fork

Hi! This is my personal fork of [paperless-gpt](https://github.com/icereed/paperless-gpt), the excellent project by [icereed](https://github.com/icereed) that brings LLMs and OCR to [paperless-ngx](https://github.com/paperless-ngx/paperless-ngx).

There is no grand plan here and this is not a replacement for upstream. It is the upstream project plus a handful of fixes I wanted before trusting it with my own documents. **If you are looking for paperless-gpt in general, please use [upstream](https://github.com/icereed/paperless-gpt).** That is also where you will find the full documentation.

## Credit where it is due

This fork is almost entirely made of other people's work. Nearly every change here is an open pull request that someone else wrote and submitted upstream. I only picked them and merged them together, while they did the actual thinking, debugging and testing. If one of these fixes helps you, please thank them and leave a note on their pull request.

## Why this fork exists

I run paperless-ngx on a small home NAS. New documents should go through Mistral OCR, then get their title, tags, correspondent, document type, date and custom fields filled in automatically, with no manual review step.

Without a review step, a mistake is unlikely to be noticed, so I care a lot about the quiet failures. A wrong field or a lost tag can sit there for years. I also have an existing archive to process, so a single broken PDF or a short outage should not derail a long run.

## What is different from upstream

Based on upstream `main` at `1d07ced` (v0.31.0 plus three commits).

### Pull requests from the community

| Pull request | Author | What it fixes | Why I wanted it |
|---|---|---|---|
| [#1224](https://github.com/icereed/paperless-gpt/pull/1224) | [kzaoaai](https://github.com/kzaoaai) | Updates the pinned `musl-dev` version in the Dockerfile | Without it, the image does not build at all |
| [#1217](https://github.com/icereed/paperless-gpt/pull/1217) | [devtechedge](https://github.com/devtechedge) | Turns pdfcpu panics into normal errors | One malformed PDF should not send the whole app into a crash loop |
| [#1229](https://github.com/icereed/paperless-gpt/pull/1229) | [skolima](https://github.com/skolima) | An unreachable provider no longer counts against the document | A network outage is not the document's fault |
| [#1218](https://github.com/icereed/paperless-gpt/pull/1218) | [shiaho777](https://github.com/shiaho777) | Existing custom fields are overwritten correctly when a new field is added first | Otherwise a field can silently keep its old value |
| [#1206](https://github.com/icereed/paperless-gpt/pull/1206) | [pipamann](https://github.com/pipamann) | Auto OCR keeps tag changes made while it runs | My own tag edits should not be reverted |
| [#1208](https://github.com/icereed/paperless-gpt/pull/1208) | [pipamann](https://github.com/pipamann) | The same protection for the auto-tag and workflow loop | This is the path my documents actually take |

### Two small changes of my own

Both are in `ocr/mistral_provider.go` and have unit tests:

- **Retries for Mistral OCR.** Requests that get HTTP 429 or a 5xx response are retried up to five times, respecting `Retry-After`. A rate limit should not earn a document the failure tag while working through an archive.
- **No image placeholders in the text.** Mistral OCR writes links such as `![img-0.jpeg](img-0.jpeg)` into its output. They are removed, so they stay out of the document content and search.

The list of merged commits is in [PATCHES.md](PATCHES.md).

## Current state

To be upfront about what has and has not been tried:

- The Go test suite passes.
- The mock-LLM end-to-end tests passed against paperless-ngx 3.4.2 on `v0.31.0-nas.1`, before my two Mistral changes were added.
- I have **not** run this in production or against the real Mistral API yet.

Think of it as a work in progress for my own setup. There is no support and there are no stability promises, and I may rework or drop this fork at any time. If upstream merges these pull requests, I would happily go back to the original.

## Using it

Configuration is the same as upstream, so the [upstream README](https://github.com/icereed/paperless-gpt#readme) applies. I do not publish a ready-made image at the moment, so you would build your own:

```bash
docker build -t paperless-gpt:local .
```

## Thanks

To [icereed](https://github.com/icereed) for creating and maintaining paperless-gpt, to the authors listed above for their fixes, and to the paperless-ngx team for the software all of this builds on.

## License

MIT, same as upstream. See [LICENSE](LICENSE).
