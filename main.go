// Copyright 2019 Philip Lombardi
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/google/uuid"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"text/template"
	"time"

	"github.com/go-chi/chi"
	"github.com/go-chi/chi/middleware"
	"github.com/gorilla/websocket"
	"github.com/openzipkin/zipkin-go"
	zipkinhttp "github.com/openzipkin/zipkin-go/middleware/http"
	"github.com/openzipkin/zipkin-go/model"
	reporterhttp "github.com/openzipkin/zipkin-go/reporter/http"
	"github.com/plombardi89/gozeug/randomzeug"
)

var port = 8080

var authCount = 0

const (
	EnvPORT        = "PORT"
	EnvHOST        = "HOST"
	EnvTLS         = "ENABLE_TLS"
	EnvOpenAPIPath = "OPENAPI_PATH"
	EnvRPS         = "RPS"
	EnvZipkin      = "ZIPKIN_SERVER"
	EnvZipkinPort  = "ZIPKIN_PORT"
	EnvDebugZipkin = "ZIPKIN_DEBUG"
	EnvConsulIP    = "CONSUL_IP"    // The IP of the Consul Pod                           #OPTIONAL - Consul Integration
	EnvPodIP       = "POD_IP"       // The IP of this pod                                 #OPTIONAL - Consul Integration
	EnvServiceName = "SERVICE_NAME" // The Name of the service (default: quote-consul)    #OPTIONAL - Consul Integration
	EnvFilePath    = "FILE_PATH"    // The path where files will be stored				  #OPTIONAL - defaults to storing images in the container /images/ folder
)

type Server struct {
	id       string
	host     string
	port     int
	tls      bool
	router   *chi.Mux
	upgrader websocket.Upgrader
	hub      *Hub
	random   *randomzeug.Random
	quotes   []string
	reqTimes []time.Time
	ready    bool
}

type QuoteResult struct {
	Server string    `json:"server"`
	Quote  string    `json:"quote"`
	Time   time.Time `json:"time"`
}

type DebugInfo struct {
	Server     string              `json:"server"`
	Time       time.Time           `json:"time"`
	Method     string              `json:"method"`
	Host       string              `json:"host"`
	Proto      string              `json:"proto"`
	URL        *url.URL            `json:"url"`
	RemoteAddr string              `json:"remoteaddr"`
	Headers    map[string][]string `json:"headers`
	Body       string              `json:"body`
}

// Health check component of the ConsulPayload struct
type HealthCheck struct {
	HTTP     string `json:"HTTP"`
	Dereg    string `json:"DeregisterCriticalServiceAfter"`
	Interval string `json:"Interval"`
}

// information for registering with consul
type ConsulPayload struct {
	Name        string      `json:"Name"`
	Address     string      `json:"Address"`
	Port        int         `json:"Port"`
	HealthCheck HealthCheck `json:"Check"`
}

type FileList struct {
	FileList []string `json:"FileList"`
}

var tmpl = template.Must(template.
	New("logout.html").
	Funcs(template.FuncMap{
		"trimprefix": strings.TrimPrefix,
	}).
	Parse(`<!DOCTYPE html>
<html>
	<head>
		<meta charset="utf-8">
		<title>Demo logout microservice</title>
	</head>
	<body>
		<fieldset><legend>SSR</legend>
			{{ if eq (len .RealmCookies) 0 }}
				<p>Not logged in to any realms.</p>
			{{ else }}
				<ul>{{ range .RealmCookies }}
					<li>
						<form method="POST" action="/.ambassador/oauth2/logout" target="_blank">
							<input type="hidden" name="realm" value="{{ trimprefix .Name "ambassador_xsrf." }}" />
							<input type="hidden" name="_xsrf" value="{{ .Value }}" />
							<input type="submit" value="log out of realm {{ trimprefix .Name "ambassador_xsrf." }}" />
						</form>
					</li>
				{{ end }}</ul>
			{{ end }}
		</fieldset>
		<fieldset><legend>JS</legend>
			{{ .JSApp }}
		</fieldset>
	</body>
</html>
`))

const jsApp = `<div id="app">
	<ul>
		<li v-for="(val, key) in realmCookies">
			<form method="POST" action="/.ambassador/oauth2/logout" target="_blank">
				<input type="hidden" name="realm" v-bind:value="key.slice('ambassador_xsrf.'.length)" />
				<input type="hidden" name="_xsrf" v-bind:value="val" />
				<input type="submit" v-bind:value="'log out of realm '+key.slice('ambassador_xsrf.'.length)" />
			</form>
		</li>
	</ul>
</div>
<script type="module">
	import Vue from 'https://cdn.jsdelivr.net/npm/vue/dist/vue.esm.browser.js';

	function getCookies() {
		let map = {};
		let list = decodeURIComponent(document.cookie).split(';');
		for (let i = 0; i < list.length; i++) {
			let cookie = list[i].trimStart();
			let eq = cookie.indexOf('=');
			let key = cookie.slice(0, eq);
			let val = cookie.slice(eq+1);
			map[key] = val;
		}
		return map;
	}

	new Vue({
		el: '#app',
		data: function() {
			return {
				"cookies": getCookies(),
			};
		},
		computed: {
			"realmCookies": function() {
				let ret = {};
				for (let key in this.cookies) {
					if (key.indexOf("ambassador_xsrf.") == 0) {
						ret[key] = this.cookies[key];
					}
				}
				return ret;
			},
		},
	});
</script>
`

func buildTracer(zipkinEndpoint string) (*zipkin.Tracer, error) {
	reporter := reporterhttp.NewReporter(zipkinEndpoint)
	localEndpoint := &model.Endpoint{ServiceName: "quote", Port: 8080}
	sampler, err := zipkin.NewCountingSampler(1)
	if err != nil {
		log.Println(err)
		return nil, err
	}
	tracer, err := zipkin.NewTracer(
		reporter,
		zipkin.WithSampler(sampler),
		zipkin.WithLocalEndpoint(localEndpoint),
	)
	if err != nil {
		log.Println(err)
		return nil, err
	}

	return tracer, err
}

// Registers this service with Consul if the environment variables are present
func RegisterConsul(quotePort int) {

	// Check if our environment variables for this function are set
	consulIP := os.Getenv(EnvConsulIP)
	if consulIP == "" {
		log.Println("CONSUL_IP environment variable not found, continuing without Consul registration")
		return
	}
	podIP := os.Getenv(EnvPodIP)
	if podIP == "" {
		log.Println("POD_IP environment variable (this pod's IP) not found, continuing without Consul registration")
		return
	}

	svcName := getEnv(EnvServiceName, "quote-consul")
	if svcName == "" {
		log.Println("SERVICE_NAME environment variable (quote service) not found, continuing with default service name \"quote-consul\"")
		log.Println("SERVICE_NAME required if not using default service name, add this if you are seeing \"no healthy upstream\" or 503 errors")
	}

	log.Println("Beginning Consul Service registration...")

	consulUrl := fmt.Sprintf("%s%s%s", "http://", consulIP, ":8500/v1/agent/service/register")
	log.Println("Consul service registration URL: ", consulUrl)

	healthCheckUrl := fmt.Sprintf("%s%s%s%d%s", "http://", podIP, ":", quotePort, "/health")
	log.Println("Health check URL:", healthCheckUrl)

	// Part of the JSON payload we are creating below to register the service with Consul
	healthCheck := HealthCheck{
		HTTP:     healthCheckUrl,
		Dereg:    "1m",
		Interval: "30s",
	}
	payload := ConsulPayload{
		Name:        svcName,
		Address:     podIP,
		Port:        quotePort,
		HealthCheck: healthCheck,
	}

	log.Println("Service registration payload: ", payload)

	// Marshal the payload to JSON
	payloadJson, err := json.MarshalIndent(payload, "", "    ")
	if err != nil {
		log.Println("Error generating Consul registration payload JSON: ", err)
		return
	}

	// Setup Http client to make request
	requesterClient := &http.Client{}

	// Method is put, and the body is our marshaled JSON in bytes
	consulRequest, err := http.NewRequest(http.MethodPut, consulUrl, bytes.NewBuffer(payloadJson))
	if err != nil {
		log.Println("Error building Consul request: ", err)
		return
	}

	// Set header and Make the request
	consulRequest.Header.Set("Content-Type", "application/json; charset=utf-8")
	consulResponse, err := requesterClient.Do(consulRequest)
	if err != nil {
		log.Println("Error submitting request to Consul: ", err)
		return
	}

	log.Println("Consul response code: ", consulResponse.StatusCode)
	if consulResponse.StatusCode != 200 {
		log.Println("Error in response from Consul service not registered successfully")
		return
	}

	successMsg := fmt.Sprintf("%s%s%s%s", "Successfully registered service:", svcName, "to Consul with IP:", podIP)
	log.Println(successMsg)
	return
}

func (s *Server) GetRPS() int {
	n := time.Now()

	count := 0

	for _, t := range s.reqTimes {
		d := n.Sub(t)
		if d.Seconds() <= 1 {
			count += 1
		}
	}
	return count
}

func (s *Server) GetQuote(w http.ResponseWriter, r *http.Request) {
	if rpsString := os.Getenv(EnvRPS); rpsString != "" {
		rps, err := strconv.Atoi(rpsString)
		if err != nil {
			log.Fatalln(err)
		}
		s.reqTimes = append(s.reqTimes, time.Now())
		if s.GetRPS() >= rps {
			http.Error(w, "Request Overload", http.StatusInternalServerError)
			return
		}
	}

	quote := s.random.RandomSelectionFromStringSlice(s.quotes)
	//quote := "Service Preview Rocks!"
	res := QuoteResult{
		Server: s.id,
		Quote:  quote,
		Time:   time.Now().UTC(),
	}

	resJson, err := json.MarshalIndent(res, "", "    ")
	if err != nil {
		log.Println(err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	w.Header().Set("content-type", "application/json")
	w.WriteHeader(http.StatusOK)

	if _, err := w.Write(resJson); err != nil {
		log.Panicln(err)
	}
}

func (s *Server) StreamQuotes(w http.ResponseWriter, r *http.Request) {
	hdr := make(map[string][]string)
	val := make([]string, 1)
	val[0] = "quote-cookie=ws"
	hdr["set-cookie"] = val

	conn, err := s.upgrader.Upgrade(w, r, http.Header(hdr))
	if err != nil {
		log.Println(err)
		return
	}

	client := &Client{hub: s.hub, conn: conn, send: make(chan []byte, 256)}
	client.hub.register <- client

	go client.readPump()
	go client.writePump()
}

func (s *Server) HealthCheck(w http.ResponseWriter, r *http.Request) {
	if s.ready {
		w.Write([]byte("OK"))
	} else {
		w.WriteHeader(http.StatusInternalServerError)
	}
}

func (s *Server) Sleep(w http.ResponseWriter, r *http.Request) {
	sleepTime := 1

	sleeps, ok := r.URL.Query()["sleep"]

	if !ok || len(sleeps[0]) < 1 {
		log.Println("Sleep parameter is missing. Using default 1 second.")
	} else {
		var err error
		sleepTime, err = strconv.Atoi(sleeps[0])
		if err != nil {
			log.Println(err)
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte("400: Sleep param is not an integer"))
			return
		}
	}

	time.Sleep((time.Duration(sleepTime) * time.Second))

	if s.ready {
		log.Printf("Slept %d seconds\n", sleepTime)
		//w.WriteHeader(http.StatusCreated)
		w.Write([]byte("200: OK\n"))
	} else {
		log.Printf("Error: Not ready. Should not have gotten request\n")
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("503: Terminating\n"))
	}
}

func (s *Server) Trace(w http.ResponseWriter, r *http.Request) {
	log.Println("Tracing request initiated")

	// Create a new request to forward
	// Read the original body
	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		log.Printf("Error reading body: %v", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	// Restore the body for potential later use
	r.Body = io.NopCloser(bytes.NewBuffer(bodyBytes))

	// Create the forwarded request (replace TARGET_URL with your desired destination)
	updatedUrl := strings.Replace(r.URL.Path, "/trace", "", 1)

	tls := "https://"

	if r.TLS == nil {
		tls = "http://"
	}

	fullUrl := tls + r.Host + updatedUrl

	log.Println("Sending request to " + fullUrl)

	forwardReq, err := http.NewRequest(r.Method, fullUrl, bytes.NewBuffer(bodyBytes))
	if err != nil {
		log.Printf("Error creating forward request: %v", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	// Copy all headers from original request
	for name, values := range r.Header {
		for _, value := range values {
			forwardReq.Header.Add(name, value)
		}
	}

	uuid, err := uuid.NewRandom()

	if err != nil {
		log.Println("Error generating UUID: ", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	forwardReq.Header.Add("x-client-trace-id", uuid.String())

	// Send the forwarded request
	client := &http.Client{}
	resp, err := client.Do(forwardReq)
	if err != nil {
		log.Printf("Error forwarding request: %v", err)
		w.WriteHeader(http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	// Copy the response status and headers back to the original client
	for name, values := range resp.Header {
		for _, value := range values {
			w.Header().Add(name, value)
		}
	}
	w.WriteHeader(resp.StatusCode)

	// Copy the response body back to the original client
	if _, err := io.Copy(w, resp.Body); err != nil {
		log.Printf("Error copying response body: %v", err)
	}
}

func (S *Server) TestAuth(w http.ResponseWriter, r *http.Request) {
	if authCount > 0 {
		w.WriteHeader(http.StatusOK)
		authCount = 0
		return
	}
	w.WriteHeader(http.StatusInternalServerError)
	authCount++
}

func (s *Server) Debug(w http.ResponseWriter, r *http.Request) {
	var bBytes []byte
	if r.Body != nil {
		bBytes, _ = ioutil.ReadAll(r.Body)
		r.Body = ioutil.NopCloser(bytes.NewBuffer(bBytes))
	}

	bString := string(bBytes)

	req := DebugInfo{
		Server:     s.id,
		Time:       time.Now().UTC(),
		Method:     r.Method,
		Host:       r.Host,
		Proto:      r.Proto,
		URL:        r.URL,
		RemoteAddr: r.RemoteAddr,
		Headers:    r.Header,
		Body:       bString,
	}

	reqJson, err := json.MarshalIndent(req, "", "    ")
	if err != nil {
		log.Println(err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	log.Println(string(reqJson))

	if strings.Compare(r.URL.Path, "/add_header") == 0 {
		w.Header().Set("x-custom-header", "true")
		w.Header().Set("set-cookie", "quote-cookie=REST")
	}

	w.Header().Set("content-type", "application/json")
	w.WriteHeader(http.StatusOK)

	if _, err := w.Write(reqJson); err != nil {
		log.Panicln(err)
	}
}

func (s *Server) Logout(w http.ResponseWriter, r *http.Request) {
	var realmCookies []*http.Cookie
	for _, cookie := range r.Cookies() {
		if strings.HasPrefix(cookie.Name, "ambassador_xsrf.") {
			realmCookies = append(realmCookies, cookie)
		}
	}
	sort.Slice(realmCookies, func(i, j int) bool {
		return realmCookies[i].Name < realmCookies[j].Name
	})
	w.Header().Set("Content-Type", "text/html; text/html; charset=utf-8")
	tmpl.Execute(w, map[string]interface{}{
		"RealmCookies": realmCookies,
		"JSApp":        jsApp,
	})
}

func GetFileContentType(out *os.File) (string, error) {

	// Only the first 512 bytes are used to sniff the content type.
	buffer := make([]byte, 512)

	_, err := out.Read(buffer)
	if err != nil {
		return "", err
	}

	// Use the net/http package's handy DectectContentType function. Always returns a valid
	// content-type by returning "application/octet-stream" if no others seemed to match.
	contentType := http.DetectContentType(buffer)

	return contentType, nil
}

func (s *Server) Upload(w http.ResponseWriter, r *http.Request) {
	log.Println("Uploading File...")

	envFilePath := os.Getenv(EnvFilePath)
	if envFilePath == "" {
		envFilePath = "/images/"
	}

	io.WriteString(w, "Upload files\n")

	file, handler, err := r.FormFile("file")
	if err != nil {
		log.Println("ERROR: Could not find file in client upload request: ", err)
		w.WriteHeader(http.StatusBadRequest)
		w.Header().Set("Content-Type", "text/html; text/html; charset=utf-8")
		w.Write([]byte("Unable to read file from request"))
		return
	}
	defer file.Close()

	// Cant overwrite edgy.jpg
	if handler.Filename == "edgy.jpeg" {
		log.Println("ERROR: Client tried to overwrite dummy file: ")
		w.WriteHeader(http.StatusForbidden)
		w.Header().Set("Content-Type", "text/html; text/html; charset=utf-8")
		w.Write([]byte("Sorry, you can't overwrite edgy.jpg"))
		return
	}

	// build the path for saving the file
	filePath := fmt.Sprintf("%s%s", envFilePath, handler.Filename)
	log.Println("Saving uploaded file to path: ", filePath)

	// copy example
	f, err := os.OpenFile(filePath, os.O_WRONLY|os.O_CREATE, 0666)
	if err != nil {
		log.Println("ERROR: Could not write file from client: ", filePath)
		w.WriteHeader(http.StatusInternalServerError)
		w.Header().Set("Content-Type", "text/html; text/html; charset=utf-8")
		w.Write([]byte("Error saving file to local storage"))
		return
	}
	defer f.Close()
	io.Copy(f, file)
	log.Println("SUCCESS, file uploaded to path: ", filePath)
	w.WriteHeader(http.StatusOK)
	w.Header().Set("Content-Type", "text/html; text/html; charset=utf-8")
	w.Write([]byte("File uploaded successfully"))
}

func (s *Server) Download(w http.ResponseWriter, r *http.Request) {

	envFilePath := os.Getenv(EnvFilePath)
	if envFilePath == "" {
		envFilePath = "/images/"
	}

	fileName := path.Base(r.URL.Path)
	filePath := fmt.Sprintf("%s%s", envFilePath, fileName)

	// check if they want edgy and overwrite the filepath
	if fileName == "edgy.jpeg" {
		filePath = "/images/edgy.jpeg"
	}

	// Open the File
	f, err := os.Open(filePath)
	if err != nil {
		log.Println("ERROR: Client requested file not found: ", filePath)
		w.WriteHeader(http.StatusNotFound)
		w.Header().Set("Content-Type", "text/html; text/html; charset=utf-8")
		w.Write([]byte("Could not find file locally"))
		return
	}
	defer f.Close()

	// Set up a buffer for the header of the file from the first 512 bytes
	FileHeader := make([]byte, 512)

	// Grab info about the file to send to the client
	f.Read(FileHeader)
	FileContentType := http.DetectContentType(FileHeader)
	FileStat, _ := f.Stat()
	FileSize := strconv.FormatInt(FileStat.Size(), 10)

	//Send the headers
	w.Header().Set("Content-Disposition", "attachment; filename="+fileName)
	w.Header().Set("Content-Type", FileContentType)
	w.Header().Set("Content-Length", FileSize)

	// Set offset back to 0 (from the 512 bytes we read)
	f.Seek(0, 0)

	// Copy to the client
	io.Copy(w, f)

	return

}

func (s *Server) ListFiles(w http.ResponseWriter, r *http.Request) {
	log.Println("Listing Files...")

	envFilePath := os.Getenv(EnvFilePath)
	if envFilePath == "" {
		envFilePath = "/images/"
	}

	files := []string{}

	if envFilePath != "/images/" {
		files = append(files, "edgy.jpeg")
	}

	_, err := os.Stat(envFilePath)
	if !os.IsNotExist(err) {
		err := filepath.Walk(envFilePath, func(path string, info os.FileInfo, err error) error {
			if !info.IsDir() {
				file := filepath.Base(path)
				files = append(files, file)
			}
			return nil
		})
		if err != nil {
			log.Println("Error reading files from directory")
		}
	}

	payload := FileList{
		FileList: files,
	}

	log.Println("File list payload:", payload)

	// Marshal the payload to JSON
	payloadJson, err := json.MarshalIndent(payload, "", "    ")
	if err != nil {
		log.Println("Error generating file list payload JSON: ", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	w.Header().Set("content-type", "application/json")
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write(payloadJson); err != nil {
		log.Panicln(err)
	}

}

func (s *Server) ConfigureRouter() {

	// Optional zipkin integration. Must set env variables for the code to run
	if zipkinServer := os.Getenv(EnvZipkin); zipkinServer != "" {

		defZipkinPort := "9411"
		zipkinPort, err := strconv.Atoi(getEnv(EnvZipkinPort, defZipkinPort))
		if err != nil {
			log.Println(err)
		}

		zipkinEndpoint := fmt.Sprintf("%s%s%s%d%s", "http://", zipkinServer, ":", zipkinPort, "/api/v2/spans")

		tracer, err := buildTracer(zipkinEndpoint)
		if err != nil {
			log.Println("Could not build Zipkin Tracer: ", err)
			log.Println("Zipkin traces disabled, check environment variables. Continuing...")
		} else {
			s.router.Use(
				zipkinhttp.NewServerMiddleware(
					tracer,
					zipkinhttp.SpanName("quote_request_span")),
			)
		}
	}

	s.router.Use(middleware.Recoverer)
	s.router.Use(middleware.RequestID)
	s.router.Use(middleware.RealIP)

	s.router.Get("/", s.GetQuote)
	s.router.Head("/", s.GetQuote)
	s.router.Get("/get-quote/", s.GetQuote)
	s.router.HandleFunc("/ws", s.StreamQuotes)
	s.router.Delete("/debug/", s.Debug)
	s.router.Post("/debug/", s.Debug)
	s.router.Put("/debug/", s.Debug)
	s.router.Get("/debug/*", s.Debug)
	s.router.Options("/debug/*", s.Debug)
	s.router.Post("/health", s.HealthCheck)
	s.router.Get("/health", s.HealthCheck)
	s.router.Get("/auth/*", s.TestAuth)
	s.router.Get("/logout", s.Logout)
	s.router.Get("/sleep/*", s.Sleep)
	s.router.HandleFunc("/trace/*", s.Trace)

	// These two endpoints can be enabled without a volume claim since we will serve a image that ships with the container
	s.router.Get("/files/", s.ListFiles)
	s.router.Get("/files/*", s.Download)

	envFilePath := os.Getenv(EnvFilePath)
	if envFilePath == "" {
		envFilePath = "/images/"
		log.Println("No FILE_PATH environment variable set, images will be uploaded to the container...")
	}

	// File uploading endpoints require a check to see if a volume is mounted
	defaultFolder, err := os.Stat(envFilePath)
	if !os.IsNotExist(err) {
		log.Println("Found storage directory: ", defaultFolder)
		log.Println("enabling file upload endpoints")

		s.router.Put("/files/*", s.Upload)
		s.router.Post("/files/*", s.Upload)
	} else {
		log.Println("Default directory not detected, disabling file upload endpoints")
	}

	s.router.Get(getEnv(EnvOpenAPIPath, "/.ambassador-internal/openapi-docs"), s.GetOpenAPIDocument)
}

func (s *Server) Start() error {
	s.hub = newHub(s.random, s.quotes, s.id)
	go s.hub.run()

	listenAddr := fmt.Sprintf("%s:%d", s.host, s.port)
	log.Printf("listening on %s\n", listenAddr)
	if s.tls {
		return http.ListenAndServeTLS(listenAddr, "/certs/cert.pem", "/certs/key.pem", s.router)
	}
	return http.ListenAndServe(listenAddr, s.router)
}

func main() {
	tls, err := strconv.ParseBool(getEnv(EnvTLS, "false"))
	if err != nil {
		log.Println("ERROR: ENABLE_HTTPS environment variable must be either 'true' or 'false'.")
	}
	defPort := "8080"
	if tls {
		defPort = "8443"
	}
	port, err := strconv.Atoi(getEnv(EnvPORT, defPort))
	if err != nil {
		log.Fatalln(err)
	}

	if port < 1 || port > 65535 {
		log.Fatalln("Server port must be in range 1..65535 (inclusive)")
	}

	startingQuotes := []string{
		"Abstraction is ever present.",
		"A late night does not make any sense.",
		"A principal idea is omnipresent, much like candy.",
		"Nihilism gambles with lives, happiness, and even destiny itself!",
		"The light at the end of the tunnel is interdependent on the relatedness of motivation, subcultures, and management.",
		"Utter nonsense is a storyteller without equal.",
		"Non-locality is the driver of truth. By summoning, we vibrate.",
		"A small mercy is nothing at all?",
		"The last sentence you read is often sensible nonsense.",
		"668: The Neighbor of the Beast.",
	}

	random := randomzeug.NewRandom()
	s := Server{
		id:     generateServerID(random),
		host:   os.Getenv(EnvHOST),
		port:   port,
		tls:    tls,
		router: chi.NewRouter(),
		upgrader: websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool {
				return true
			},
			ReadBufferSize:  1024,
			WriteBufferSize: 1024,
		},
		random: random,
		quotes: startingQuotes,
		ready:  true,
	}

	// Check for Consul integration & register the service with Consul
	RegisterConsul(s.port)

	s.ConfigureRouter()

	// Handle SIGTERM gracefully
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGTERM)

	go func(r *bool) {
		<-signals
		*r = false
		fmt.Printf("SIGTERM received. Marked unhealthy and waiting to be killed.\n")
	}(&s.ready)

	log.Fatalln(s.Start())
}
