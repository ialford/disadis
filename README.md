Disadis
=======

[![APACHE 2
License](http://img.shields.io/badge/APACHE2-license-blue.svg)](./LICENSE)
[![Go Report
Card](https://goreportcard.com/badge/github.com/ndlib/disadis)](https://goreportcard.com/report/github.com/ndlib/disadis)

Disadis is an download proxy for Hydra-based applications.
It will proxy content out of a Fedora 3 instance, so your Ruby application
doesn't have to devote a valuable app instance to doing an otherwise mindless
task.
The way we do this is have the rails application handle the download request
initially, and then, if the user is authorized, redirect to disadis by way of an
nginx internal redirect.
Then disadis will start and monitor the actual download to the client.

Features of Disadis include

* provides E-tags based on datastream version numbers
* responds to `GET` and `HEAD` requests
* handles range requests
* forces the allowable datastreams to download to be whitelisted
* assumes the filename is the label of the datastream
* can handle an arbitrary number of simultaneous downloads
* uses a minimal amount of memory since all downloads are streamed

# Use

The daemon will listen on several ports for incoming HTTP requests.
The exact ports and the number of them is determined by the configuration file.
Each port can have a number of _handlers_ attached to it.
Usually each datastream name you wish to proxy will have its own handler.
On each port requests are expected to have the form `/:id` or `/:id?datastream_id=XXX`.
The `id` can have some prefixed attached to it, and then fedora is checked for
the object and the given datastream, with the content being proxied back if it
exists.

# Configuration

The daemon takes a command line argument which names a configuration file.
The file gives how to determine the current user from a request, the handlers to
set up, and the URL to use to address fedora. All logging is sent to `STDOUT`.

The configuration file consists of a number of sections, which may appear in any order.
The first section `[general]` has two variables to set:

 * `fedora-addr` is the root URL to use to access your fedora instance.
 It should include the fedora username and password if those are needed to download content from your fedora.
* `bendo-token` is a token to use for content stored at external URLs via E or R datastreams. (optional)

Sample section:

    [general]
    fedora-addr = http://fedoraAdmin:fedoraAdmin@localhost:8983/fedora

The other sections each specify a handler.
There will be as many additional sections as you need for each handler.
The section name is `[Handler "name"]` where `name` is the name you want to use for this handler.
Inside the section there are a few variables to set for that handler.

 * `port` is the port number disadis should listen on for this handler.
 * `versioned` is whether disadis should support the versioned url. One of `true` or `false`. Defaults to `false`.
 * `prefix` is the prefix, if any, to add to the identifier in the URL.
 * `Datastream` is the datastream to proxy of the item in fedora.
 * `Datastream-id` is the `datastream_id` name you want to associate this handler with.
 Either not setting it or using the name `default` makes this the handler used when there is
 no `datastream_id` parameter on the incoming request.

A sample handler would look like

    [Handler "thumbnail"]
    datastream = thumbnail
    prefix = sufia:
    port = 4000
    datastream-id = thumbnail

This configuration will have disadis listen to localhost:4000, and any requests
of the form `/{id}?datastream_id=thumbnail` will result in the download of the
datastream `thumbnail` from the object `sufia:{id}`.

## Example

A complete configuration file would look similar to the following.

```
[general]
fedora-addr = http://fedoraAdmin:fedoraAdmin@localhost:8983/fedora

[Handler "thumbnail"]
datastream = thumbnail
prefix = sufia:
port = 4000
datastream-id = thumb

[Handler "dl"]
datastream = content
port = 4000
prefix = sufia:
```

This configuration will have disadis listen on port 4000 for connections.
HTTP requests to path `/{id}` result in the download of the `content` datastream
of the fedora object `sufia:{id}`.
Requests to the path `/{id}?datastream_id=thumb` result in the download of
the `thumbnail` datastream.

## Versioned

If a datastream handler is has `versioned` set to `true`, then
paths of the form `/{id}/{version}` are handled, where `version` refers
to the integer fedora datastream number.
Requests without a version are assigned the most current version for that datastream.
For the moment, requests to versions besides the most current version are denied
with a 404 error.

# Nginx Redirects

The nginx internal redirect is handled by first defining an internal location in
your nginx config file.
The following block provides a template you can use.

```
location ^~ /download-internal/ {
    internal;
    proxy_set_header Host              $host;
    proxy_set_header X-Real-IP         $remote_addr;
    proxy_set_header X-Forwarded-For   $proxy_add_x_forwarded_for;
    proxy_set_header X-Forwarded-Proto $http_x_forwarded_proto;
    proxy_redirect   off;
    proxy_buffering  off;
    proxy_pass       http://127.0.0.1:4000/;
}
```

And then the rails application can pass control to the disadis daemon
by setting the header `X-Accel-Redirect` to the route `/download-internal/{id}`
and then returning without writing a response body.
The following code in Rails shows one way of doing it.
(In this case the fedora id is in the variable `asset.noid`)

    response.headers['X-Accel-Redirect'] = "/download-internal/#{asset.noid}"
    head :ok

Nginx will then send the request to disadis.
The client does not see any of the internal redirects--as far as the client is
concerned, there is only a single request and a single response.

# Future

* Is there a simpler way to configure the whole thing? It seems too complicated to me.
* Support config reloading and graceful shutdowns
