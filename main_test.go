package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

var buckets = [2]string{"pcs.inconnu.app","pcs.botch.lol"}

// Create a faceclaim upload request for a given bucket
func createFaceclaimRequest(bucket string) *FaceclaimRequest {
	return &FaceclaimRequest{
		Guild: 0,
		User: 0,
		CharID: "__test",
		ImageURL: "https://tilt-assets.s3-us-west-1.amazonaws.com/tiltowait.webp",
		Bucket: bucket,
	}
}

// Quiet logs and set the faceclaim bucket name
func TestMain(m *testing.M) {
	log.SetOutput(ioutil.Discard)
	FaceclaimBucket = "pcs.inconnu.app"
	gin.SetMode(gin.TestMode)

	os.Exit(m.Run())
}

func TestEnvVars(t *testing.T) {
	// No env vars set
	assert.NotNil(t, prepareEnvVars(), "prepareEnvVars() should have returned an error")

	// Only first env var set
	os.Setenv("API_TOKEN", "")
	assert.NotNil(t, prepareEnvVars(), "prepareEnvVars() should have returned an error")

	// Only second env var set
	os.Unsetenv("API_TOKEN")
	os.Setenv("FACECLAIM_BUCKET", FaceclaimBucket) // Don't overwrite the current value
	assert.NotNil(t, prepareEnvVars(), "prepareEnvVars() should have returned an error")

	// Both env vars set
	os.Setenv("API_TOKEN", "")
	assert.Nil(t, prepareEnvVars(), "prepareEnvVars() should have passed")

	// Reset for later tests
	os.Unsetenv("API_TOKEN")
	os.Unsetenv("FACECLAIM_BUCKET")
}

// Test that authentication checks work by briefly setting the API_TOKEN var
func TestBadAuth(t *testing.T) {
	os.Setenv("API_TOKEN", "test")
	r := setupRouter(false)
	w := performRequest(r, "POST", "/faceclaim/upload", nil)

	os.Unsetenv("API_TOKEN") // Unset so we don't break future tests
	assert.Equal(t, 401, w.Code)
}

// Ensure that we get a 400 error if the faceclaim can't be processed
func TestFaceclaimEmptyUpload(t *testing.T) {
	r := setupRouter(false)
	w := performRequest(r, "POST", "/faceclaim/upload", nil)

	assert.Equal(t, 400, w.Code)
}

func TestFaceclaimCorrectUpload(t *testing.T) {
	r := setupRouter(false)

	for _, bucket := range buckets {
		request, _ := json.Marshal(createFaceclaimRequest(bucket))
		w := performRequest(r, "POST", "/faceclaim/upload", bytes.NewBuffer(request))

		assert.Equal(t, 201, w.Code)

		// Check that the image exists at the URL
		imgUrl := getStringBody(w.Body)
		assert.True(t, urlExists(imgUrl), fmt.Sprintf("%v does not exist", imgUrl))
		assert.True(t, strings.HasPrefix(imgUrl, fmt.Sprintf("https://%v", bucket)))
	}
}

func TestSingleDelete(t *testing.T) {
	r := setupRouter(false)

	for _, bucket := range buckets {
		// Process a new faceclaim
		request, _ := json.Marshal(createFaceclaimRequest(bucket))
		w := performRequest(r, "POST", "/faceclaim/upload", bytes.NewBuffer(request))

		assert.Equal(t, 201, w.Code)

		// Make sure the image was, in fact, created
		imgUrl := getStringBody(w.Body)
		assert.True(t, urlExists(imgUrl), fmt.Sprintf("%v does not exist", imgUrl))
		assert.True(t, strings.HasPrefix(imgUrl, fmt.Sprintf("https://%v", bucket)))

		// Now delete the faceclaim
		path := fmt.Sprintf("/faceclaim/delete/%v/%v", bucket, getObjectFromUrl(imgUrl))
		w = performRequest(r, "DELETE", path, nil)
		assert.Equal(t, 200, w.Code)

		successful := false
		for i := 0; i < 60; i++ {
			if urlExists(imgUrl) {
				successful = true
				break
			}
			time.Sleep(1 * time.Second)
		}
		assert.True(t, successful, "Image was not deleted after 60 seconds")
	}
}

func TestMultiDelete(t *testing.T) {
	r := setupRouter(false)

	for _, bucket := range buckets {
		// Shared request will result in different ObjectIds from being created
		faceclaimRequest := createFaceclaimRequest(bucket)
		request, _ := json.Marshal(faceclaimRequest)

		// Upload three images
		imgUrls := make([]string, 3)
		for i := 0; i < 3; i++ {
			w := performRequest(r, "POST", "/faceclaim/upload", bytes.NewBuffer(request))

			// Make sure the images were created successfully
			assert.Equal(t, 201, w.Code)
			url := getStringBody(w.Body)
			assert.True(t, urlExists(url), "The image was not uploaded")
			assert.True(t, strings.HasPrefix(url, fmt.Sprintf("https://%v", bucket)))
			imgUrls[i] = url
		}

		// Delete the entire Faceclaim group
		path := fmt.Sprintf("/faceclaim/delete/%v/%v/all", bucket, faceclaimRequest.CharID)
		w := performRequest(r, "DELETE", path, nil)
		assert.Equal(t, 200, w.Code)

		// Ensure the URLs were all deleted
		numDeleted := 0
		successful := false
		for n := 0; n < 60; n++ {
			numDeleted = 0
			for _, u := range imgUrls {
				if !urlExists(u) {
					numDeleted += 1
				} else {
					// No point in checking the other URLs if one still exists
					break
				}
			}
			if numDeleted == len(imgUrls) {
				successful = true
				break
			}
			time.Sleep(1 * time.Second)
		}
		assert.True(t, successful, "The faceclaim images were not deleted after 60s")
	}
}

func TestLogUpload(t *testing.T) {
	fileName := "main.go"
	var b bytes.Buffer
	m := multipart.NewWriter(&b)
	f, _ := os.Open(fileName)
	defer f.Close()

	fw, _ := m.CreateFormFile("log_file", fileName)
	io.Copy(fw, f)
	m.Close()

	r := setupRouter(false)
	req := httptest.NewRequest("POST", "/log/upload", &b)
	req.Header.Set("Content-Type", m.FormDataContentType())
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusCreated, w.Code)
}

// HELPERS

func getStringBody(r io.Reader) string {
	bodyBytes, _ := io.ReadAll(r)
	var body string
	json.Unmarshal(bodyBytes, &body)

	return body
}

func performRequest(r http.Handler, method, path string, body io.Reader) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(method, path, body)
	r.ServeHTTP(w, req)

	return w
}

func getObjectFromUrl(url string) string {
	// In actual use, CharID is an ObjectId, just like the WebP file's name.
	// Thus, this regex should NOT be considered valid for standard use.
	// During testing, however, we use a hardcoded "__test" CharID for easy
	// identification in the GCP console.
	r := regexp.MustCompile("([A-Za-z0-9_]+\\/[A-Fa-f0-9]+\\.webp)$")
	match := r.FindStringSubmatch(url)
	return match[0]
}

func urlExists(url string) bool {
	r, err := http.Head(url)
	if err != nil {
		return false
	}
	return r.StatusCode == 200
}
