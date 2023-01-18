A simple REST API for [Inconnu](https://github.com/tiltowait/inconnu). Adapted from [the initial Python version](https://github.com/tiltowait/inconnu-api-python).

## Endpoints

### `/faceclaim/upload` (POST)

Upload a character "faceclaim" image. Requires the following payload:

* **guild:** The Discord server on which the character exists
* **user:** The Discord user uploading the image
* **charid:** The character's database ID
* **image_url:** The URL where the image can currently be found

When this endpoint runs, it downloads the image from the URL, converts it to WebP at 99% quality, and uploads it to Google Cloud Storage.

### `/faceclaim/delete/{charid}/all` (DELETE)

Delete all of a character's faceclaim images. This is accomplished by publishing a message to Pub/Sub, which triggers a Cloud Function that handles the actual deletion. This has the benefit of a speedup over waiting for GCS to find all the blobs belonging to the character and deleting them one-by-one.

### `/faceclaim/delete/{charid}/{key}` (DELETE)

Delete a single faceclaim image found at `{charid}/{key}`. As above, this is accomplished by a Pub/Sub-triggered Cloud Function.

### `/log/upload` (POST)

Uploads a log file to GCS for archival storage.

## Why an API?

(Why not?) There are a few reasons:

1. Get practice with something new to me.
2. Offload image processing from the bot process. This allows the image downloading, WebP conversion, and image uploading to happen asynchronously.
3. Make the bot code vendor-agnostic. With an API as an abstraction, the bot won't need to know about whichever cloud vendor (currently GCP) is in use.

## Why the rewrite?

It was slow.

No, really.

While I've had great results using FastAPI in the past, startup speed on Google Cloud Run, where it was hosted, was very, very bad—ranging from 4 to 15 seconds! I tried a rewrite in Node, but it was only marginally better. This Go implementation has a cold start of roughly 1-1.5 seconds, which is perfectly acceptable here.

It's possible that FastAPI was the culprit, and that using Flask might have accomplished the same thing without learning an entirely new programming language. (It was also the far less interesting solution.)
