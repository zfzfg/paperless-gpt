# paperless-gpt, a small personal fork

Hi! This is my personal fork of [paperless-gpt](https://github.com/icereed/paperless-gpt), the excellent project by [icereed](https://github.com/icereed) that brings LLMs and OCR to [paperless-ngx](https://github.com/paperless-ngx/paperless-ngx).

There is no grand plan here and this is not a replacement for upstream. It is the upstream project plus the fixes I wanted before trusting it with my own documents. **If you are looking for paperless-gpt in general, please use [upstream](https://github.com/icereed/paperless-gpt).** That is also where you will find the full documentation.

## Credit where it is due

This fork builds on other people's work. Six of its fixes are open pull requests that someone else wrote and submitted upstream. For those, I only picked them and merged them together, while the authors did the actual thinking, debugging and testing. If one of these fixes helps you, please thank them and leave a note on their pull request.

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

### Changes of my own

All of these have unit tests.

**Mistral OCR**

- **Retries for Mistral OCR.** Requests that get HTTP 429 or a 5xx response are retried up to five times, respecting `Retry-After`. A rate limit should not earn a document the failure tag while working through an archive.
- **No image placeholders in the text.** Mistral OCR writes links such as `![img-0.jpeg](img-0.jpeg)` into its output. They are removed, so they stay out of the document content and search.
- **No paying twice for OCR.** Automatic runs reuse the pages of a failed previous run, and skip OCR when the document already carries the text of its last run. Both apply for 24 hours. Before this, an outage sent finished pages to the provider again on every retry: in a test with five pages, a metadata outage over three polls caused 15 page requests instead of 5.

**Metadata from Mistral**

- **A direct chat client for `LLM_PROVIDER: mistral`.** The client from langchaingo fails when a model returns `content` as an array of chunks, which reasoning models may do. A reply that is empty or cut off now counts as an error instead of leaving the document looking finished. It also no longer forces temperature 1 and `max_tokens` 4000 onto requests, so the model's own defaults apply.

**Created date**

- **No guessing.** The default prompt used to ask for today's date when it found none, and for the first of the month or January when parts were missing. It now answers `unknown`, which leaves the existing date alone.
- **Impossible dates are dropped.** That covers a date after the document was added ([#1089](https://github.com/icereed/paperless-gpt/issues/1089)), with one day of leeway for time zones, and today's date when the document already has a different one. A date clearly printed in the document still replaces an existing date.

**Outages and retries**

- **Shorter, cancellable waits.** The wait after a failed poll is capped at 5 minutes instead of an hour, and resets once a poll gets documents through. A failure in the OCR queue no longer stops the auto-tag queue in that poll.
- **An outage is not the document's fault.** HTTP 401, 402, 403, 429 and 5xx get a limit of 48 attempts instead of the normal retry limit, so a provider outage does not put the failure tag on a whole queue within minutes. That is about four hours at the longest wait.
- **Permanent errors are not resent.** Other 4xx responses to chat requests, such as a 400, fail right away.

**Custom fields**

- **JSON wrapped in prose is understood**, such as a sentence followed by a fenced block.
- **Unusable replies are visible.** The other fields are still written, and the document gets the failure tag instead of counting as done.
- **Empty values are skipped**, so "nothing found" cannot clear or block an existing value.
- **Numbers for text fields become strings.** paperless-ngx answers a bare number in a string field with HTTP 500, which loses the whole update.

The list of merged commits and changes is in [PATCHES.md](PATCHES.md).

## Current state

To be upfront about what has and has not been tried:

- The Go test suite passes.
- The mock-LLM end-to-end tests passed against paperless-ngx 3.4.2 on `v0.31.0-nas.1`, before my own changes were added. They have not been run again since.
- The new date, retry, chat client and custom field changes have only run against test servers and mocks. The field limits and the HTTP 500 were checked against a throwaway local paperless-ngx.
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
