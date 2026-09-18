`mailer` is a mail delivery service. It lets you schedule mail jobs, with attachments, for future dates and then does its best to deliver them.


## Methods

As of right now there are two methods that you can call on Mailer: PUT + DELETE

PUT: Send up a MailRequest (json) to schedule a job

To schedule a new job, call `/job` as PUT

Successful submissions return JSON with `success: true` and `code: 200` after
the job is stored. Check the JSON result as well as the HTTP status: this API can
return `success: false` in an HTTP 200 response.

Retries with the same `job_key`, `to_addr`, and `title` acknowledge the existing
job without changing its contents, schedule, attempt count, or delivery state.
This includes jobs already sent. Keep all three values stable when retrying a
request whose response was lost. Changing the recipient or title creates a
different job; changing only the body does not update an existing job. Acceptance
means the queue owns the job, not that the recipient received it. This protection
lasts while the queue record exists; deleting it removes the duplicate marker.

For release/newsletter mail, an optional `unsubscribe_url` supplies the caller's
HTTPS one-click unsubscribe endpoint. The mailer stores it with the job and adds
`List-Unsubscribe: <URL>` and `List-Unsubscribe-Post: List-Unsubscribe=One-Click`
to Mailgun and SendGrid messages. URLs with credentials, fragments, or header
delimiters are rejected. The caller must implement the unsubscribe POST; the
mailer never fetches this URL. Omit this field for transactional payment receipts.
Older queue records default to no unsubscribe URL. This adds headers only:
callers must also use the suppression API below to cancel queued work.

### Durable suppression

Before enabling an Alerts client, authenticate `GET /capabilities` using the same
timestamp scheme (sign the GET method and `/capabilities` path). The response
advertises `queue_contract: 1` and the features `idempotent_jobs`,
`unsubscribe_headers`, and `durable_suppression`, alongside `success` and `code`.
Clients must require these features rather than assuming an older deployment
honors new JSON fields. The check is read-only and does not test database health,
provider credentials, or actual delivery. Deploy and verify those separately.

Authenticated `PUT /suppression` accepts `{"scope":"job","key":"job-key"}` or
`{"scope":"subscription","key":"subscription-generation"}`. It atomically
stores a permanent suppression marker and marks matching unsent work cancelled.
Repeated requests succeed. A later submission matching the marker succeeds as a
no-op; it cannot recreate the cancelled job, even after restart. Sent records
remain sent. Suppression markers have no automatic expiry.

Use opaque keys. A new opt-in must use a new subscription-generation key; the
old generation remains suppressed so delayed requests cannot reactivate it.
Subscription suppression does not affect transactional mail that omits that key.
The worker checks cancellation again after batch selection, immediately before
starting the provider request. An already-started provider request cannot be
recalled, but late results cannot restore retries for a cancelled job.

The legacy DELETE endpoints still remove existing records; they do not provide
this durable protection against future submissions. Alerts must integrate the
suppression API before enabling queued delivery for subscriber notifications.

```
curl https://localhost:8889/job -X PUT \
	--data '{"job_key": "keyless", \
	  "to_addr": "based@example.com", \
	  "title": "test email", \
	  "text_body": "see attached", \
	  "html_body": "<head><body><p>see attached!</p></body></html>",\ 
	  "attachments": ["H4sIAAAAAAAA/2LkZmBgSM7PK0nNK9ErqShh4mJgYChJrSjRL8hJzMxjFmdgYMhLLVeAKlEoyVcoL8osSVXIzAMEAAD//2GY2D47AAAA"], \
	  "send_at": 1680376820}' \
	-H "Authorization: <token>,
	-H "X-Base58-Timestamp: 1680395128"
```

Attachments are a base64 encoded string of a proprietary encoding of the attachment file name, content-type, and content; see the `mailer/types.go` for details on how these are packed and encoded. Note: if you're not using gzip, you're doing it wrong.


To cancel a job, you'd send the `job_key` up in a DELETE.

```
curl https://localhost:8889/job -X DELETE \
	--data '{"job_key": "keyless"}' \
	-H "Authorization: <token>,
	-H "X-Base58-Timestamp: 1680395128"
```

This will delete all unsent + failed jobs for the given `job_key`. Note that it's inteded that series of mailers might have the same `job_key`, e.g. all the emails that you'd expect to get before an event or Base58 course.


### Authorization

The endpoints are guarded by a ~dragon~ HMAC secret. The secret requires a timestamp be passed in in the header `X-Base58-Timestamp` in UNIX time, seconds resolution. It'll use this along with the HTTP Method, path, and a shared HMAC secret to figure out if this is a valid request or not.

You probably don't have the HMAC secret, so you're probably not authorized to hit the deployed version of this. But feel free to ship your own with your own secrets!


NO WARRANTY IMPLIED, GUARANTEED TO BE FAULTY.

### Read-only queue monitoring

Set a separate `METRICS_TOKEN` in the runtime secrets file to enable `GET /metrics`.
Scrapes require `Authorization: Bearer <METRICS_TOKEN>`; this token cannot submit
or cancel email. Without a token the endpoint returns 404, invalid tokens return
401, and datastore failures return 503 rather than reporting an empty queue.

`mailer_jobs{state=...}` exposes fixed states: pending, in_flight, retrying,
exhausted (20 attempts), provider_accepted and cancelled. No recipient, subject,
job key, subscription key or mail domain appears in labels. Provider acceptance
is not inbox delivery and does not capture later bounces or complaints.
`mailer_oldest_attempt_overdue_seconds` measures lateness against the current
scheduled attempt time; a retry changes that schedule, so this is not total
message age. Metrics are read-only and never claim a delivery batch.

Deploy a separate Prometheus scrape with the read-only bearer credential and route
exhausted-retry and overdue-attempt alerts independently of this email pipeline.
The Alerts repository supplies tested rules in `deploy/alerts-rules.yml`. Verify
real scraping, routing and provider bounce handling before launch.

### Mailgun bounce and complaint feedback

Set `MAILGUN_WEBHOOK_SIGNING_KEY` (the Mailgun webhook signing key, not the API key
or queue secret) and configure permanent-failure and complaint webhooks to POST
to `https://<mailer-host>/webhooks/mailgun`. Without this key the route is disabled.
The configured `MAIL_DOMAINS` list restricts accepted sending domains. HTTPS is
required at the reverse proxy because Mailgun signs timestamp/token, not the full
JSON event body. Signature verification follows
https://documentation.mailgun.com/docs/mailgun/user-manual/webhooks/securing-webhooks.

New Mailgun messages include a `mailer_queue_id` variable. Feedback must match
that stored queue identity, recipient and configured sending domain. Explicit
permanent hard bounces and complaints persist a recipient/domain block, cancel
queued messages in that domain and prevent future sends. The send boundary checks
the actual provider domain, including configured fallback routing. Messages already
submitted to the provider cannot be recalled. Other domains remain unaffected;
within the blocked domain this also protects transactional senders from repeatedly
mailing an undeliverable or complaining recipient. It does not change application
newsletter/release consent or billing access.

Invalid signatures/associations return 406; successful commits and duplicates return
200; storage failures return 503 so Mailgun can retry. Hashed replay tokens are
pruned in bounded batches after two days during feedback processing; full webhook
payloads are never retained. Recipient blocks persist until an operator-reviewed
recovery procedure is implemented. Pre-integration emails without the queue variable
cannot be matched. Real provider verification, operator recovery, and surfacing the
blocked destination to Alerts remain launch work.
