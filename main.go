// A simple API for Inconnu.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"time"

	"cloud.google.com/go/pubsub"
	"cloud.google.com/go/storage"
	"github.com/gin-gonic/gin"
	"github.com/nickalie/go-webpbin"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

const ProjectID = "inconnu-357402"
var ApiToken string
var Port string
var FaceclaimBucket string

// JSON represents generic k:v pairings used by publishMessage().
type JSON map[string]interface{}

// A FaceclaimRequest represents the necessary POST body data for /faceclaim/upload.
type FaceclaimRequest struct {
	Guild    int    `json:"guild"`
	User     int    `json:"user"`
	CharID   string `json:"charid"`
	ImageURL string `json:"image_url"`
	Bucket 	 string	`json:"bucket"`
}

func main() {
	log.SetFlags(log.Ltime | log.Lmicroseconds)
	if err := prepareEnvVars(); err != nil {
		log.Fatalln(err.Error())
	}

	r := setupRouter(true)
	r.Run(":" + Port)
}

// Gets the ApiToken and FaceclaimBucket vars from the environment.
func prepareEnvVars() error {
	port, ok := os.LookupEnv("PORT")
	if ok {
		Port = port
	} else {
		// $PORT doesn't need to explicitly be set
		Port = "8080"
	}

	if token, ok := os.LookupEnv("API_TOKEN"); ok {
		ApiToken = token
	} else {
		return errors.New("API_TOKEN is not set!")
	}
	if bucket, ok := os.LookupEnv("FACECLAIM_BUCKET"); ok {
		FaceclaimBucket = bucket
	} else {
		return errors.New("FACECLAIM_BUCKET is not set!")
	}
	return nil
}

// Sets up the router. Disable Gin logging by setting showLogs to false.
func setupRouter(showLogs bool) *gin.Engine {
	var r *gin.Engine
	if showLogs {
		r = gin.Default()
	} else {
		// For testing purposes. No logger, no recovery, no default routes
		r = gin.New()
	}

	r.SetTrustedProxies(nil)
	r.Use(VerifyAuth())

	r.POST("/faceclaim/upload", processFaceclaim)
	r.DELETE("/faceclaim/delete/:bucket/:charid/all", deleteCharacterFaceclaims)
	r.DELETE("/faceclaim/delete/:bucket/:charid/:key", deleteSingleFaceclaim)
	r.POST("/log/upload", uploadLog)

	return r
}

// VerifyAuth ensures that the Authorization token matches the API_KEY ENV var.
func VerifyAuth() gin.HandlerFunc {
	return func(c *gin.Context) {
		token := c.Request.Header.Get("Authorization")
		if token != os.Getenv("API_TOKEN") {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
			return
		}
		c.Next()
	}
}

// ROUTES

// Downloads a given image, then converts it to WebP and uploads it to GCS.
func processFaceclaim(c *gin.Context) {
	var request FaceclaimRequest
	if err := c.BindJSON(&request); err != nil {
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	objectURL, err := processImage(request)
	if err != nil {
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusCreated, objectURL)
}

// Publishes a delete-faceclaim-group message to Pub/Sub to delete all of a
// character's faceclaim images in the background. This is done to speed up the
// response of this function, as the user doesn't need to see the deletions
// happen in real time.
func deleteCharacterFaceclaims(c *gin.Context) {
	bucket := c.Param("bucket")
	charid := c.Param("charid")
	if err := publishMessage("delete-faceclaim-group", JSON{"bucket": bucket, "charid": charid}); err != nil {
		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, fmt.Sprintf("Deleted %v's faceclaim images", charid))
}

// Publishes a delete-single-faceclaim message to Pub/Sub to delete a given GCS
// object in the background. This is probably no faster, from a user's POV,
// than deleting the object here; however, this setup allows us to delete all
// of a character's faceclaim images using the same mechanism, which is much
// more responsive for the user.
func deleteSingleFaceclaim(c *gin.Context) {
	bucket := c.Param("bucket")
	charid := c.Param("charid")
	key := c.Param("key")
	object := fmt.Sprintf("%v/%v", charid, key)

	if err := publishMessage("delete-single-faceclaim", JSON{"key": object, "bucket": bucket}); err != nil {
		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, fmt.Sprintf("Deleted %v", object))
}

// Upload a log file to the "inconnu-logs" bucket in GCS. This route will
// overwrite any object by the same name!
func uploadLog(c *gin.Context) {
	formFile, err := c.FormFile("log_file")
	if err != nil {
		c.AbortWithStatusJSON(http.StatusBadRequest, err.Error())
		return
	}
	fileData, err := formFile.Open()
	if err != nil {
		c.AbortWithStatusJSON(http.StatusBadRequest, err.Error())
		return
	}
	defer fileData.Close()

	uploadObject(fileData, "inconnu-logs", formFile.Filename, "text/plain")
	c.JSON(http.StatusCreated, fmt.Sprintf("Uploaded %v", formFile.Filename))
}

// GCP HELPERS

func publishMessage(topicName string, data JSON) error {
	ctx := context.Background()

	// Get the client
	pubsubClient, err := pubsub.NewClient(ctx, ProjectID)
	if err != nil {
		return fmt.Errorf("pubsub.NewClient: %v", err)
	}

	// Get the topic
	pubsubTopic := pubsubClient.Topic(topicName)
	log.Println("pubsubTopic:", pubsubTopic)

	// The client is valid; now format the message
	msg, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("json.Marshal: %v", err)
	}
	log.Println("Message JSON:", string(msg))

	res := pubsubTopic.Publish(ctx, &pubsub.Message{Data: msg})
	if _, err := res.Get(ctx); err != nil {
		return fmt.Errorf("Publish.Get: %v", err)
	}
	log.Println("Queued deletion of", data)

	return nil
}

func processImage(request FaceclaimRequest) (string, error) {
	resp, err := http.Get(request.ImageURL)
	if err != nil {
		return "", fmt.Errorf("http.Get: %v", err)
	}
	defer resp.Body.Close()

	log.Println("File downloaded; converting to WebP")

	var buf bytes.Buffer
	err = webpbin.NewCWebP().
		Quality(99).
		Input(resp.Body).
		Output(&buf).
		Run()
	if err != nil {
		return "", fmt.Errorf("webpbin: %v", err)
	}
	log.Println("File converted!")

	// Determine the bucket to upload to
	bucketName := request.Bucket
	if bucketName == "" {
		log.Println("Bucket not specified; using", FaceclaimBucket)
		bucketName = FaceclaimBucket
	} else {
		log.Println("Using specified bucket", bucketName)
	}

	// The objectName is <charid>/<ObjectId()>.webp
	o := primitive.NewObjectID()
	objectName := fmt.Sprintf("%v/%v.webp", request.CharID, o.Hex())

	// Upload the file
	metadata := map[string]string{
		"guild":    fmt.Sprint(request.Guild),
		"user":     fmt.Sprint(request.User),
		"original": request.ImageURL,
		"charid":   request.CharID,
	}
	if err = uploadObject(&buf, bucketName, objectName, "image/webp", metadata); err != nil {
		return "", fmt.Errorf("processImage: %v", err)
	}

	// The object's URL is derived from the bucket name and key name
	return fmt.Sprintf("https://%v/%v", bucketName, objectName), nil
}

// Adapted from https://cloud.google.com/storage/docs/uploading-objects-from-memory
func uploadObject(data io.Reader, bucket, object, contentType string, metadata ...map[string]string) error {
	// bucket := "bucket-name"
	// object := "object-name"
	ctx := context.Background()
	client, err := storage.NewClient(ctx)
	if err != nil {
		return fmt.Errorf("storage.NewClient: %v", err)
	}
	defer client.Close()

	ctx, cancel := context.WithTimeout(ctx, time.Second * 50)
	defer cancel()

	// Upload an object with storage.Writer
	wc := client.Bucket(bucket).Object(object).NewWriter(ctx)
	wc.ContentType = contentType
	wc.ChunkSize = 0

	if len(metadata) > 0 {
		wc.Metadata = metadata[0]
	}

	if _, err = io.Copy(wc, data); err != nil {
		return fmt.Errorf("io.Copy: %v", err)
	}
	if err := wc.Close(); err != nil {
		return fmt.Errorf("Writer.Close: %v", err)
	}

	log.Printf("%v uploaded to %v\n", object, bucket)

	return nil
}
