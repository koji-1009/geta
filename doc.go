// Package geta is a file-routed HTTP framework whose skeleton is fixed before
// the program runs.
//
// What can be decided before a request arrives — which URLs exist, what runs
// in which order, what each operation receives and returns, what it requires —
// is written as typed values that the compiler checks or that [New] checks
// once at assembly. What can only be known at run time, such as whether a
// token is valid or a row exists, is checked per request.
//
// A route is a function that returns a [Route]. Its methods are fields, each
// holding an [Operation] built by [Op] or [OpNoBody], which infer the input
// and output types from the handler. Those types are the contract: the
// schema, the validation, and the OpenAPI document all derive from them.
//
// Each string format geta checks has a type, such as [Date], [IPv4], or
// [Duration]. A format whose grammar belongs to the application, such as
// email, is carried by the application's type ([FormatType]). A request is
// held to its document: the backstops of [Limits] appear as bounds in the
// request's schemas. Response schemas state only what the author declared;
// getatest checks response bodies against them ([App.Conforms]), and geta
// does not check them at run time.
//
// An input that embeds [Conditional] answers conditional requests (304, 412,
// and 428 with [RequireConditional]). Middleware runs in the scopes of a
// route's directories and, for a single operation, in its [Doc.Scope].
//
// A request body is JSON (body:"json"), a form (body:"form"), or
// multipart/form-data (body:"multipart"), whose file parts are [File] values.
// Another media type, or a content coding other than identity, answers 415
// with Accept or Accept-Encoding listing what the operation takes. geta
// answers OPTIONS on every path an operation serves, with Allow, under the
// root scope; OPTIONS * lists every method served, and any other method in
// that form is a 400. [Compress] codes responses in the codings the
// application plugs in, with gzip built in ([Gzip]). The document is OpenAPI
// 3.1, or 3.2 with [WithOpenAPI]. An [App] is an http.Handler, so any server,
// including [Run] or an HTTP/3 server, can serve it.
//
// Errors are ordinary values. A handler returns whatever the layer below
// returned, and the operation's [Doc.Failures] table decides which status it
// becomes. An error that no row matches is a defect and answers 500.
package geta
