# The (Obscure) Quote of the Moment Service

This is a long-lived branch of the [quote service](https://github.com/datawire/quote) for use in the tour application.

This branch is pulled into [datawire/tour](https://github.com/datawire/tour) as the `backend/` directory via a `git subtree`.

All updates to the tour backend should be made against the [tour-master](https://github.com/datawire/quote/tree/tour-master) branch of [datawire/quote](https://github.com/datawire/quote) and pulled into tour by running:

```
git subtree pull --prefix backend https://github.com/datawire/quote tour-master --squash
```

-----
## Building the application

`docker build -t {DOCKER_USERNAME}/{IMAGE_NAME}:{VERSION} .`

Ex: `docker build -t datawire.io/quote:0.5.0 .`

-----
## Running the application

Run with Docker:

`docker run -p 8080:8080 {IMAGE}`

Pass environment variables with Docker:

`docker run -p 8080:8080 -e ENVIRONMENT_VARIABLE='value' {IMAGE}`

-----
## Environment Variables
| Name | Description | Default |
| :---: | :---: | :---: |
| PORT | What port the service should listen on | 8080 |
| ENABLE_TLS | Whether to use TLS for HTTPS or use HTTP | false |
| OPENAPI_PATH | What path to serve the OpenAPI document on | /.ambassador-internal/openapi-docs |
| ZIPKIN_SERVER | The Zipkin service for reporting traces to | N/A |
| ZIPKIN_PORT | The port for the Zipkin service | 9411 |
| CONSUL_IP | The IP address of the consul server pod for registering this service with Consul | N/A |
| POD_IP | The IP of this pod for registering this service with Consul  | N/A |
| SERVICE_NAME | The name to register this service with consul under | quote |
| FILE_PATH | The path where files will be uploaded to | /images/ |


-----
## Endpoints & making requests
> **Note:** The following curl commands assume that you have deployed this application by following the [Ambassador Edge Stack quickstart guide](https://www.getambassador.io/docs/edge-stack/latest/tutorials/getting-started). `/backend/` is the prefix for routing requests to this service, and is dropped before the request hits the `quote` service. If you are running via docker, then you will not need to add `/backend/` to any of your requests and can just use the endpoints directly.


-----
- `/`

    **GET:** Gets a randomly selected quote and a string to represent the name of the quote service.

    Ex: `curl -kv https://{IP_ADDR}/backend/`

-----
- `/get-quote/`

    **GET:** Gets a randomly selected quote and a string to represent the name of the quote service.

    Ex: `curl -kv https://{IP_ADDR}/backend/get-quote/`

-----
- `/debug/`

    **GET:** Prints headers and information about the request.

    **POST:** Prints headers and information about the request and sends the body of the request back as well.

    Ex: `curl -kv https://{IP_ADDR}/backend/debug/`


-----
- `/debug/*`

    **GET:** Functions the same as the `/debug/` path without a POST option.

    Ex: `curl -kv https://{IP_ADDR}/backend/debug/{path}`

-----
- `/health`

    **GET:** Returns a 200 OK when the service is functioning.

    Ex: `curl -kv https://{IP_ADDR}/backend/health`

-----
- `/auth/*`

    **GET:** Alternates between sending a `500 Internal Server Error` and `200 OK` response each time you make a request to this endpoint.

    Ex: `curl -kv https://{IP_ADDR}/backend/auth/*`


-----
- `/sleep/*`

    **GET:** Queries the URL for an ammount of time to sleep for before responding to the request. Defaults to one second when it is unable to parse a sleep parameter.

    Ex: `curl -kv https://{IP_ADDR}/backend/sleep/`

    Ex: `curl -kv https://{IP_ADDR}/backend/sleep/\?sleep=5`

    > **Note:** You need to escape the `?sleep` on the command line. On a browser you can exclude the `\`


-----
- `/files/`

    **GET:** returns a list of files available to be downloaded.

    **POST:** Uploads a file to the service to be downloaded later. Overwrites existing files if provided with the same name as an existing file. Uses the path as the name for the file.

    Ex: `curl -kv https://{IP_ADDR}/backend/files/`

    Ex: `curl -kv --form "file=@README.md" https://{IP_ADDR}/backend/files/`

    > **Note:** The `FILE_PATH` environment variable is used to configure a custom path for storing files if using a persistent volume in Kubernetes. Otherwise it will default to storing files ephemerally inside the container.


    > **Note:** For successful file uploads it expects the file to be passed in a value called `file` exactly as shown in the example.


-----
- `/files/*`

    **GET:** returns a file of the provided name if it exists or a 404 if it cannot find the file. The container ships with a file `edgy.jpeg` for testing.

    Ex: `curl -kv https://{IP_ADDR}/backend/files/edgy.jpeg`


-----
