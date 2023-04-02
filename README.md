`mailer` is a mail delivery service. It lets you schedule mail jobs, with attachments, for future dates and then does its best to deliver them.


## Methods

As of right now there are two methods that you can call on Mailer: PUT + DELETE

PUT: Send up a MailRequest (json) to schedule a job

To schedule a new job, call `/job` as PUT

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
