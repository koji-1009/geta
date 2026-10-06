# Claims and the tests that hold them

Every behaviour geta states has a test or a command that checks it. [`llms.txt`](../llms.txt) explains each one; this file names the test.

## Checked by the compiler

| Claim | Test |
| --- | --- |
| A handler of the wrong shape fails to compile on the `geta.Op` line | `TestCompileErrors/handlershape` |
| A URL cannot answer one method twice | `TestCompileErrors/duplicatemethod` |
| The success status cannot be omitted | `TestCompileErrors/missingstatus` |
| A route needing more of the Env than it has fails to compile | `TestCompileErrors/envrequirement` |
| A table line to a directory without `Route`, or with the wrong Env, fails to compile | `TestCompileErrors/missingroute`, `TestCompileErrors/wrongenv` |

## Checked at assembly (`geta.New`)

| Claim | Test |
| --- | --- |
| A `path` tag the URL lacks, or a URL parameter left unbound, is refused | `TestRejectsPathTagNotInURL`, `TestRejectsURLParameterNotBound`, `TestRejectsUnboundURLParameterOnEveryOperation` |
| Malformed or duplicate paths, name-only template differences, empty routes, untagged input fields, and pointer path parameters are refused | `TestRejectsMalformedPaths`, `TestUnescapedLiteralsMatchEscapedRequests`, `TestRejectsDuplicatePaths`, `TestRejectsTemplatesDifferingOnlyInNames`, `TestRejectsRouteServingNothing`, `TestRejectsUntaggedInputField`, `TestRejectsPointerPathParameter` |
| A path a client could not send as written is refused, once per mistake | `TestRejectsDotSegmentsInTemplates`, `TestRejectsLiteralsAPathCannotHold`, `TestEachAssemblyMistakeIsReportedOnce` |
| geta.New reports every mistake at once, a shared scope's or gate's once; a nil option, a zero Union, or a nil handler is a mistake, not a panic | `TestEachAssemblyMistakeIsReportedOnce`, `TestEveryAssemblyMistakeIsReportedAtOnce`, `TestRejectsDefaultSchemeWithoutVerifier`, `TestNilAndZeroArgumentsAreRefused` |
| URLs match literal-first with backtracking, in any table order; unclean paths redirect; `r.Pattern` and `r.PathValue` are set | `TestLiteralBeforeParameterWithBacktracking`, `TestMoreThanEightParameters`, `TestMatchingIgnoresTableOrder`, `TestMethodNotAllowedUnionsTheBranches`, `TestMethodsAreCaseSensitive`, `TestEncodedDotSegmentsAreOrdinarySegments`, `TestAnEscapedSlashBesideARawByteStaysInItsSegment`, `TestUncleanPathsRedirect`, `TestCleaningKeepsATrailingSlash`, `TestMiddlewareSeesPatternAndPathValues`, `TestPathValuesWhenMounted`, `TestRootRewriteIsMatchedAgain`, `TestPathValuesAfterARootRewriteAreTheServedOperations` |
| URL matching agrees with an independent reference under fuzzing (seeds in `testdata/fuzz/FuzzRouter`) | `FuzzRouter` |
| Header parameters, preconditions, content headers, and path values agree with independent references under fuzzing | `FuzzHeaderParameters`, `FuzzConditional`, `FuzzContentHeaders`, `FuzzPathValues` |
| Schema mistakes are refused | `TestRejectsSchemaMistakes` (18 cases) |
| Numeric keywords no value can meet, and bounds a float64 cannot hold, are refused | `TestRejectsUnreachableBounds`, `TestRejectsBoundsFloat64CannotHold`, `TestFloat32Range` |
| A `format=` tag names only a format geta checks, and not on a type with its own format | `TestFormatTagsAreChecked`, `TestAFormatTypeTheDocumentCannotTrustIsRefused` |
| A request lower bound, enum member, or format the backstop or pattern ceiling makes unreachable is refused | `TestLowerBoundsPastTheBackstopAreRefused`, `TestPatternCeiling`, `TestDocumentStatesTheBackstops`, `TestMapSizeKeywordsAreRefused`, `TestDocumentStatesMapSizes`, `TestADeclaredObjectIsHeldToTheBackstop`, `TestValueKeywordMistakesAreRefused`, `TestKeyKeywordMistakesAreRefused`, `TestFormatsBoundTheirOwnLength`, `TestFormatLengthsAreTheFormats`, `TestABackstopNoUUIDFitsIsRefused` |
| A length bound outside a checked format's own lengths is refused, by `geta.New` and getavet | `TestFormatsBoundTheirOwnLength`, `TestKeyFormatsMatchGetaNew` (getavet) |
| Header or cookie enum members the place cannot carry, and leap-second `time.Time` members, are refused | `TestEnumMembersAHeaderOrCookieCannotCarryAreRefused`, `TestLeapSecondEnumOfTimeIsRefused` |
| deepObject mistakes and names colliding with a deepObject's keys are refused | `TestDeepObjectMistakesAreRefused`, `TestDeepObjectsMatchGetaNew` (getavet) |
| Defaults and examples that cannot apply or that the schema refuses are refused | `TestDefaultAndExampleMistakesAreRefused`, `TestDefaultsAndDocsMatchGetaNew` (getavet) |
| Misused `geta.Nullable` is refused | `TestNullableMistakesAreRefused`, `TestNullableMatchesGetaNew` (getavet) |
| A number enum member the type or schema refuses is refused | `TestNumberEnumMistakesAreRefused` |
| A `geta.FormatType` the document cannot trust is refused | `TestAFormatTypeTheDocumentCannotTrustIsRefused` |
| Embedded input fields geta cannot bind, and header or cookie names that are not tokens, are refused | `TestRejectsEmbeddedPointerInInput`, `TestRejectsEmbeddedTextTypeInInput`, `TestRejectsSchemaTagOnEmbeddedInputStruct`, `TestRejectsSchemaTagOnEmbeddedStruct`, `TestRejectsInvalidInputCookieName`, `TestRejectsInvalidInputHeaderName`, `TestEnvelopeAndInputFieldRefusals` (getavet) |
| A body type is accepted only as `encoding/json/v2` reads and writes it | `TestMemberFieldsAsV2ReadsThem`, `TestNamedEmbeddedFieldsAreMembers`, `TestTextAndJSONMethodsAsV2CallsThem`, `TestOneSidedMapKeysAreRefused`, `TestMapKeysFollowV2ForEveryReceiver`, `TestSinglePassReadsMapKeysByTheirMethods`, `TestTimeDurationIsRefused` |
| The rules shared with getavet are `Check*` functions in geta's internal package `internal/vet` with the same text | `TestInputRefusalsAreCheckInputFields`, `TestEnvelopeRefusalsAreCheckEnvelopeFields`, `TestVetFieldAcceptsWhatNewAccepts`, `TestBodyRefusalsAreSharedRules`, `TestGenericTypeNames`, `TestCheckSchemaTag`, `TestComponentNamesMatchGetaNew`, `TestDocTimeoutMatchesGetaNew` (getavet) |
| A type whose component name OpenAPI does not allow is refused | `TestComponentNamesAreOnesOpenAPIAllows`, `TestComponentNamesMatchGetaNew` (getavet) |
| `Doc.BeforeGate` joins the chain where the first gate runs; its order is checked; a gate in it is refused | `TestBeforeGateIsPlacedAndChecked` |
| `Doc.Scope` joins the order check; a gate there secures its operation alone | `TestOperationScopeOrderIsChecked`, `TestOperationScopeGateCountsForItsOperationOnly`, `TestSecureInAnOperationScope` |
| A descending middleware chain is refused, naming both ends | `TestRejectsDescendingRootChain`, `TestRejectsDescendingAcrossTheScopeSeam`, `TestAcceptsAscendingEqualAndUnordered`, `TestApplicationStageInterleaves`, `TestVocabularyAscends` |
| Built-in middleware carry their ranks; ETag outside Gzip and CORS inside Recover are refused | `TestBuiltinsCarryTheirOrder`, `TestRejectsEtagOutsideGzip`, `TestRejectsCORSInsideRecover` |
| Middleware construction mistakes are assembly errors in every scope | `TestRejectsMiddlewareConstructionMistakes`, `TestMiddlewareAnswersMistakesAreRefused`, `TestANilLoggerIsRefused`, `TestCORSMaxAgeIsWholeSeconds`, `TestCORSOriginsAreSerializedOrigins`, `TestRejectsZeroAndInvalidMiddlewareEverywhere`, `TestMiddlewareHeaderMistakesAreRefused`, `TestMiddlewareHeaderTypeMistakesAreRefused` |
| `CORS` below the root scope is refused | `TestCORSBelowTheRootIsRefused` |
| A negative `Doc.Timeout`, or one with no `geta.Timeout` in the chain, is refused | `TestDocTimeoutMistakesAreRefused` |
| Invalid failure rows, bad problem types, and rows for `context.DeadlineExceeded` are refused | `TestRejectsInvalidFailureRows`, `TestRejectsAnInvalidProblemType`, `TestARowForTheDeadlineIsRefused` |
| An `OnAsProblem` description the document could not state is refused | `TestFailureDescriptionMistakesAreRefused`, `TestProblemDescriptionsMatchGetaNew` (getavet) |
| Schemes without a verifier, unprotected secured routes, conflicting or incomplete schemes are refused | `TestRejectsSchemeWithoutVerifier`, `TestRejectsDefaultSchemeWithoutVerifier`, `TestRejectsRootDefaultWithoutVerifierWithNoRoutes`, `TestRejectsProtectedRouteWithoutGate`, `TestRejectsConflictingSchemeDefinitions`, `TestRejectsIncompleteSchemes`, `TestSchemeMembersOf32AreRefusedIn31`, `TestSchemesWithEqualFlowsAreOneDefinition` |
| Every scheme type is documented with its members; the documents validate | `TestSecuritySchemesGolden`, `TestSecuritySchemesGolden32`, `TestSecuritySchemesWriteEveryMemberSet` |
| `Middleware.Scopes` states scopes in the document only; scope mistakes are refused | `TestScopesAreStatedInTheRequirements`, `TestScopesChangeNoRuntimeBehaviour`, `TestScopeMistakesAreRefused` |
| Inconsistent `Doc.Deprecation` and `Doc.Sunset` are refused | `TestDeprecationMistakesAreRefused` |
| Duplicate operation IDs and schema names are refused | `TestRejectsDuplicateOperationID`, `TestRejectsDerivedOperationIDCollision`, `TestRejectsOperationIDOfAnOptionsOperation`, `TestRejectsDuplicateSchemaName`, `TestReservedProblemSchemaName` |
| Form and multipart mistakes are refused | `TestRejectsFormMistakes`, `TestFormBodiesMatchGetaNew` (getavet) |
| `geta.Compress` refuses codings whose tags could not be told apart | `TestCompressRejectsCodings` |
| An unknown OpenAPI version is refused | `TestOpenAPIRefusesAnUnknownVersion` |
| Document text that is not UTF-8 is refused | `TestDocumentTextThatIsNotUTF8IsRefused` |
| A body on a `Get` or `Delete` operation is refused | `TestABodyOnGetOrDeleteIsRefused`, `TestMethodBodiesMatchGetaNew` (getavet) |
| A `Route.Query` operation needs OpenAPI 3.2 | `TestQueryNeedsOpenAPI32` |
| A status field's mistakes are refused | `TestStatusFieldMistakesAreRefused`, `TestStatusFieldMistakesLeaveNoHeaders`, `TestStatusFieldsMatchGetaNew` (getavet) |
| A raw body the document could not state is refused | `TestRawBodyMistakesAreRefused`, `TestRawBodiesMatchGetaNew` (getavet) |
| A body on 204 is refused; statuses must be 2xx/3xx but 304; streams are 200 and upgrades 101 | `TestRejects204WithBody`, `TestAccepts204WithoutBody`, `TestRejectsNonSuccessStatus`, `TestSpecialOutputsFixTheirStatus` |
| A redirect needs a `Location` header field; an empty one at runtime is a 500 | `TestRedirectsCarryALocation`, `TestARedirectWithoutALocationIsADefect`, `TestRedirectsMatchGetaNew` (getavet) |

## Checked per request

| Claim | Test |
| --- | --- |
| A wrapped error meets its row; `err.Error()` never reaches the response | `TestWrappedErrorMeetsItsRow`, `TestEmptyDetailSendsOnlyTheStatusText` |
| `Deprecation` and `Sunset` go out on every response of their operation | `TestDeprecationIsSentOnEveryResponse`, `TestDeprecationFollowsARootRewrite`, `TestDeprecatedWithoutADateSendsNoHeader`, `TestSunsetAlone`, `TestDeprecationIsDocumentedOnEveryResponse`, `TestCORSExposesDeprecation` |
| An `OnAsProblem` row answers the problem the error describes | `TestFailuresDescribeTheirProblem`, `TestADescriptionThatCannotBeWrittenIsADefect`, `TestCORSExposesDescribedHeaders` |
| Unmatched errors and handler defects are logged 500s, whatever rows match | `TestUndeclaredErrorIsA500AndIsLogged`, `TestNilOutputIsADefect`, `TestACatchAllRowDoesNotSwallowANilOutput`, `TestACatchAllRowDoesNotSwallowAConditionalDefect` |
| Every 500 carries a fresh `urn:uuid` instance, also in its log line | `TestDefectsCarryAnInstanceTheLogCarriesToo` |
| Rows are checked top to bottom; `OnAs` matches by type | `TestRowsAreCheckedTopToBottom`, `TestRowsMayBeSharedAsSlices` |
| `Failure.Type` sets the problem type; untyped rows are `about:blank` | `TestFailureTypesTellRowsApart`, `TestUntypedRowsAreAboutBlank` |
| A deadline is 504; a client that went away gets nothing | `TestDeadlineExceededIs504`, `TestDerivedContextDeadlineIs504`, `TestClientCancellationWritesNothing`, `TestDerivedContextClientGoneWritesNothing` |
| 404 and 405 are problems the root scope sees | `TestNotFoundAndMethodNotAllowedAreProblems`, `TestRootURL`, `TestScopesAndMatch`, `TestConnectAuthorityFormMatchesNothing` |
| OPTIONS answers 204 with `Allow`; `OPTIONS *` lists every method | `TestOptionsListsTheMethodsServed`, `TestOptionsUnionsTheBranches`, `TestOptionsOnAPathWithoutOperationsIs404`, `TestMethodNotAllowedListsOptions`, `TestOptionsAsteriskListsEveryMethod`, `TestOptionsAsteriskWithNoRoutesListsOptions`, `TestOptionsAsteriskThroughARealServer`, `TestAsteriskFormBehindARootGate` |
| HEAD is served by GET, and only where GET is | `TestHeadIsServedByGet`, `TestHeadWithoutGetIs405`, `TestConnectIsNeverRedirected` |
| An unclean path redirects to the clean form of the client's URL, safely escaped | `TestRedirectUnderAMountStaysInsideIt`, `TestNoRedirectWhereTheClientsURLIsUnknown`, `TestRewriteToAnUncleanPathIsServedNotRedirected`, `TestARedirectedRequestCarriesTheCleanPathsMatch`, `TestRedirectBehindARootGate`, `TestUncleanPathsRedirect`, `FuzzRouter` |
| OPTIONS runs the root scope alone, its gates checking their defaults | `TestOptionsRunsTheRootScopeOnly`, `TestRootScopeSeesOptions`, `TestOptionsBesideCORS` |
| Asterisk form on any method but OPTIONS is a 400 | `TestAsteriskFormOnOtherMethodsIs400` |
| QUERY is served with its body and listed wherever methods are | `TestQueryIsServedWithABody` |
| A root rewrite of method or path moves the match | `TestRewrittenMethodReachesTheGate`, `TestRewrittenPathMovesTheMatch`, `TestRewriteBeforeTheGateStillChecks`, `TestRewriteToAMethodNotServed`, `TestRewrittenOptionsAnswersTheNewPath`, `TestRewriteToQuery` |
| A rewrite after a root gate to an operation it did not check is a 500 | `TestRewriteAfterTheGateIsRefused`, `TestRewrittenPathAfterTheGate`, `TestRewriteAfterADetachingGateIsRefused`, `TestRefusedRewriteAfterTheGateForEachMethod`, `TestRewriteBetweenTwoRootGatesIsRefused`, `TestRefusedRewriteStatusIsDocumented` |
| Below the root scope, a rewrite moves neither the operation nor its gates | `TestRewriteInsideTheOperationDoesNotMoveTheGate` |
| A root middleware passing a detached context is a 500 | `TestADetachedContextIsRefused`, `TestADetachedContextNeverServesWithoutTheMatch`, `TestDetachedVerifierContextKeepsTheMatch` |
| `geta.Observe` reports what was served; `Documented` and `Conforms` take its `Match` | `TestObserveReportsWhatWasServed`, `TestObserveInsideTheApp`, `TestDocumentedTakesTheMatch`, `TestConformsChecksOnlyWhatTheDocumentStates` |
| `Conforms` holds a response to its declared schema, never to `Limits` | `TestConformsHoldsResponsesToTheDeclaredSchemaOnly`, `TestConformsHoldsAMapToItsDeclaredSize`, `TestKeyKeywordsHoldEachKey` |
| `Doc.BeforeGate` runs before the gate, for its operation alone | `TestBeforeGateRunsBeforeAuthentication`, `TestBeforeGateCannotBeSkippedByARewrite`, `TestLoginIsRateLimited` (auth) |
| `Doc.Scope` runs innermost, for its method, before binding | `TestOperationScopeRunsInnermostForItsMethodOnly`, `TestOperationScopeRefusesBeforeBinding` |
| Input violations are a 400 listing up to 50 violations | `TestQueryBinding`, `TestHeaderAndCookieBinding`, `TestPathBindingWithTextTypes`, `TestBodyBinding`, `TestAtMostFiftyViolationsPerResponse`, `validate_test.go` |
| A violation's path is cut to 256 bytes after `…`, keeping its end, a duplicate key's included; a message quotes at most 128 bytes of a value, then `…`; the errors array stops before 16 KiB, counted exactly, the first always listed; `omitted` counts what is not listed, is documented in the Problem schema, reserved for `OnAsProblem` rows, checked by `Conforms`, read by getaclient and counted in its `Error` text, and logged at Debug as a count; a problem under both bounds is unchanged; `Conforms` names a response's paths and values whole and counts what it does not list; a 415's detail quotes a `Content-Type` or `Content-Encoding` cut as a value is, splitting no rune | `TestViolationListsAreBounded`, `TestRefusedHeaderValuesAreClipped`, `TestClipSplitsNoRune`, `FuzzClip`, `FuzzContentHeaders`, `TestJSONStringLenIsTheWrittenLength`, `TestListViolationsBoundsTheWrittenArray`, `TestAtMostFiftyViolationsPerResponse`, `TestKeyKeywordsHoldEachKey`, `TestTheProblemComponent`, `TestConformsChecksDescribedFailures`, `TestConformsNamesResponseViolationsWhole`, `TestClientErrorCarriesOmitted` |
| A body's 413 or 415 is answered alone | `TestABodyAnsweredOnItsOwnComesBeforeParameterViolations` |
| Parameter text that is not UTF-8 is a 400 | `TestTextThatIsNotUTF8IsRefused` |
| Integer parameters are written as JSON integers: `007` and `+7` are refused | `TestIntegerParametersAreJSONIntegers` |
| A slice header is a comma-separated list | `TestHeaderListsAreReadAsTheDocumentSays` |
| `items.` keywords hold each element and are documented | `TestElementKeywordsHoldEachElement`, `TestElementKeywordMistakesAreRefused`, `TestSinglePassAgreesOnKeywords`, `TestElementKeywordsMatchGetaNew` (getavet) |
| `additionalProperties.` keywords hold each map value and are documented | `TestValueKeywordsHoldEachValue`, `TestValueKeywordMistakesAreRefused`, `TestSinglePassAgreesOnKeywords`, `TestValueKeywordsMatchGetaNew` (getavet), `TestSchemaKeywordsGolden` |
| Map keys are held to the backstop and to `propertyNames.` keywords | `TestKeyKeywordsHoldEachKey`, `TestKeyKeywordMistakesAreRefused`, `TestKeysTheTypeReadsAreHeldAsSent`, `TestSinglePassAgreesOnMapSizes`, `TestKeysAreReadUnderTheOperationsLimits`, `TestKeyKeywordsMatchGetaNew` (getavet), `TestSchemaKeywordsGolden` |
| A key type's format is stated in `propertyNames` and held | `TestAKeyTypesFormatIsStatedAndHeld`, `TestAKeyTypeIsJudgedAsAValueOfItsType`, `TestKeyFormatsMatchGetaNew` (getavet) |
| `minProperties` and `maxProperties` hold a request's member count | `TestMapSizeKeywordsHoldARequest`, `TestSinglePassAgreesOnMapSizes`, `TestMapSizeKeywordsAreRefused`, `TestCheckSchemaTag`, `TestSchemaTagsOnObjectsMatchGetaNew` (getavet), `TestSchemaKeywordsGolden`, `TestADeclaredObjectIsHeldToTheBackstop` |
| Untruthful enums, discriminator defaults, and discriminator schemas are refused | `TestEnumMembersTheFormatOrTypeRefusesAreRefused`, `TestDiscriminatorsAreTruthful` |
| Unknown members, duplicate keys, wrong-case names, and trailing data are refused | `TestBodyBinding`, `TestParseRejectsAmbiguousJSON`, `TestObjectPaths`, `TestContractViolationsAre400` |
| Numbers are checked exactly as written | `TestFloatBoundsCompareTheWrittenNumber`, `TestIntegerBoundsAreExact`, `TestMultipleOfIsExact`, `TestUniqueItemsComparesNumbersExactly`, `TestCanonicalNumbers` |
| Fixed-size integers and float32 document their own range | `TestSchemaBoundsKeepTheIntegerRange`, `TestFloat32Range` |
| A number enum compares values exactly as written | `TestNumberEnumsAreCheckedExactly`, `TestSinglePassAgreesOnKeywords`, `FuzzSinglePass`, `FuzzParamFastPath` |
| Format types accept exactly what the JSON Schema Test Suite accepts; `format_suite_test.go` lists no divergence | `TestFormatTypesAgreeWithTheJSONSchemaTestSuite`, `TestFormatTypesReadTheirGrammars`, `TestFormatTypesZeroValues`, `TestDateAndTimeOfDay`, `TestDateTime`, `TestDuration`, `TestNetworkFormats`, `TestPointerFormats` |
| Application-specific formats (`email`, ...) are carried by the application's `geta.FormatType` | `TestAnApplicationTypeCarriesTheFormatEmail`, `TestAnApplicationEmailRoundTrips`, `TestAnApplicationTypeMayCarryAFormatGetaChecks` |
| `geta.DateTime` takes leap seconds; `time.Time`'s schema refuses them | `TestDateTime`, `TestTimeSchemaAdmitsWhatTimeHolds`, `TestLeapSecondEnumOfTimeIsRefused` |
| Format types bind, write, document, and travel through the typed client | `TestFormatTypesBindRequests`, `TestFormatTypesInTheDocument`, `TestAppendTextWritesHeadersAndBodies`, `TestTypedClientCarriesFormatTypes` |
| A `format=` string is held as its format type holds text | `TestFormatTagsAreChecked`, `TestFormatChecksAgreeWithTheFormatTypes` |
| `time.Time` is read by RFC 3339's grammar | `TestTimeTakesExactlyRFC3339DateTimes`, `TestDateTimeIsRFC3339` |
| `geta.Password` is written in full as JSON but printed `[redacted]` | `TestPasswordIsRedacted`, `TestFormatTypesInTheDocument` |
| Path parameter names follow ServeMux's rule, in any script | `TestPathParameterNamesInAnyScript` |
| Backstops: string, pattern, array, nesting, body size | `TestStringBackstop`, `TestPatternCeiling`, `TestArrayBackstop`, `TestNestingCeiling`, `TestSinglePassHoldsMaxDepthInsideAnyJSON`, `TestBodyLimitIs413AtTheExactBoundary` |
| `Doc.Limits` are the operation's own, in reading, buffering, and the document | `TestOperationLimitsAreTheOperationsOwn`, `TestOperationLimitsKeepComponentsTruthful`, `TestOperationLimitsBufferTheResponse` |
| `WithLimits` replaces `DefaultLimits` whole; a zero cap is refused | `TestWithLimitsRefusesAZeroCap` |
| Content of the wrong media type is a 415 naming the right one | `TestUnsupportedMediaTypeIs415` |
| Content sent to an operation that reads no body is a 415 | `TestContentToAnOperationWithoutABodyIs415` |
| Content in a content coding is a 415 with `Accept-Encoding: identity` | `TestContentCodingIs415`, `TestRefusedContentIs415PastTheBodyLimit`, `TestDecodedContentIsTaken` |
| A declared length is judged before any content is read | `TestDeclaredContentIsRefusedBeforeItIsRead`, `TestADeclaredLengthLeavesAReaderToItsHandler`, `TestAReplacedBodyIsReadNotJudgedByItsHeaders` |
| geta's own refusal of an unread body closes the HTTP/1.x connection at once | `TestARefusalDoesNotWaitForAnUnreadBody`, `TestARefusalClosesTheConnectionAtOnce`, `TestARefusalOfAReadBodyKeepsTheConnection` |
| Other answers leaving the body unread drain it within the Timeout's window | `TestAStalledBodyAfterAnAnswerEndsItsConnectionAfterTheWindow`, `TestABodyInTheWindowAfterAnAnswerKeepsItsConnection`, `TestABodyReadWholeTakesNoDeadlineAfterTheAnswer`, `TestAStalledBodyAfterAnAnswerWithoutATimeoutIsNetHTTPs` |
| A handler chooses among its declared success statuses | `TestHandlersChooseAmongDeclaredStatuses` |
| A raw request body is read whole or handed over as a reader | `TestRawRequestBodies` |
| A raw response body goes out with its media type; a reader is always closed | `TestRawResponseBodies`, `TestARawReaderIsClosedOnEveryPath`, `TestRawBodyEdges`, `TestARawBodyThroughGzipAndETag` |
| Form and multipart bodies bind; a cut-short multipart body is a 400 | `TestFormBinding`, `TestOptionalFormBody`, `TestMultipartBinding`, `TestAMultipartBodyCutShortIsRefused`, `TestMultipartTemporaryFilesAreRemoved`, `TestOversizedFormIs413`, `TestClientSendsFormsAndFiles` |
| A multipart body of more parts than MaxItems is a 400, whatever the parts name | `TestMultipartFilesAndTheirStore` |
| A body of no declared length past MaxBodyBytes is a 413 wherever the excess lies, even where the multipart reader fails first; a store failure stays a 500 | `TestOversizedFormIs413`, `TestAFileGetaCannotWriteIsA500`, `FuzzMultipartParts` |
| The multipart close delimiter is watched for in time linear in the body, whatever the boundary's length | `TestCloseWatcherScanIsLinear`, `BenchmarkCloseWatcherBoundary` |
| A request schema states the backstops; a response schema does not; such types split into two components | `TestDocumentStatesTheBackstops`, `TestPatternCeiling`, `TestHeaderAndCookieParametersDocumentWhatTheyCarry`, `TestDocumentShape`, `TestComponentsSplitOnlyWhereTheSidesDiffer`, `TestSealedTypeRoundTrip`, `TestDocumentStatesMapSizes`, `TestADeclaredMaxPropertiesLeavesOneComponent`, `TestADeclaredObjectIsHeldToTheBackstop`, `TestKeyKeywordsHoldEachKey` |
| Header and cookie parameters are documented and held as what their place carries | `TestHeaderAndCookieParametersDocumentWhatTheyCarry`, `TestHeaderParametersHoldToWhatAHeaderCarries`, `TestPreconditionHeadersCarryTheirLists`, `TestCarriersHoldWhatTheirPatternsMatch`, `TestCookieCarrierIsWhatNetHTTPReads`, `TestHeaderCarrierIsWhatNetHTTPReceives`, `TestCarriedFormatsCarryEveryValue` |
| An output header value no header can carry is a 500 | `TestOutputHeadersHoldToWhatAHeaderCarries`, `TestNonFiniteNumberHeadersAreDefects` |
| `geta.Conditional` evaluates preconditions in RFC 9110 §13.2.2's order | `TestConditionalEvaluationOrder`, `TestETagAndCheckReadIfNoneMatchAlike`, `TestPreconditionsAreReadByTheirGrammarAlike`, `TestPreconditionHeadersCarryTheirLists`, `TestNotModifiedCarriesTheValidator`, `TestPreconditionProblems`, `TestEmptyPreconditionListsAreRefused`, `TestDatePreconditionsAreReadAsTheGrammarWritesThem`, `TestStarIsAloneButForSpacesAndTabs`, `TestPreconditionErrorStatus`, `TestCheckRefusesATagThatIsNotOne`, `TestPreconditionErrorElsewhereIsADefect`, `TestConditionalBesideTheETagMiddleware`, `TestConditionalHeaderBoundTwiceIsRefused`, `TestRequireConditionalCheckAbsentEvaluates`, `TestDeclaredStatusesBesideConditional` |
| No input reaches the author's 500; the binder never panics | `FuzzBinding` |
| No body reaches the author's 500 through a sealed type; accepted bodies round-trip | `FuzzSealed` |
| Success JSON carries `X-Content-Type-Options: nosniff` | `TestSuccessJSONIsNosniff` |
| Small bodies are buffered with Content-Length; large ones stream; write failures abort | `TestLargeBodiesStream`, `TestLargeEnvelopes`, `TestABufferedBodyThatFailsIsA500`, `TestResponseBufferBounds`, `TestACommittedWriteFailureAborts` |
| A streamed body that fails to encode aborts the connection | `TestAStreamedBodyThatFailsAbortsTheConnection` |
| A large response holds at most 64 KiB of its body | `TestLargeBodiesHoldBoundedMemory`, `BenchmarkLargeGet` |
| A request body is read once; responses are written by `encoding/json/v2` | `TestRejectsSchemaMistakes`, `TestEncodeShapesMatchTheSchema` |
| A type holds itself only through a named struct; any other self-holding type is refused, whatever order its types are met in | `TestTypesHoldingThemselvesOutsideANamedStructAreRefused`, `TestTypesHoldingThemselvesThroughANamedStructAreTakenInAnyOrder`, `TestBodiesOfTypesHoldingThemselvesAreReadAtDepth`, `TestSinglePassReadsTypesHoldingThemselvesAtDepth`, `TestSelfHoldingTypesMatchGetaNew` (getavet), `TestSelfHoldingTypesInAnyOrderMatchGetaNew` (getavet) |
| A type with its own JSON methods needs no declaration | `TestOwnJSONTypesNeedNoDeclaration`, `TestOwnJSONOutputThatIsNotJSONIsADefect`, `TestOwnJSONTypesUseTheirMethods`, `TestWithSchemaMistakes`, `TestSinglePassTakesNullForAnOwnJSONType`, `TestSinglePassRefusesNullForADeclaredOwnJSONType`, `TestSinglePassAgreesOnKeywords`, `FuzzSinglePass` |
| Sealed types are checked by discriminator, documented as `oneOf`, and refused when misdeclared | `TestSealedTypeRoundTrip`, `TestSealedTypeViolations`, `TestSealedTypeOutputDefects`, `TestSealedTypeDeclarationMistakes`, `TestNilUnionInsideAPointerVariant`, `TestTypedNilPointerVariantIsADefect`, `TestNilUnionNamesItsPath`, `TestNilUnionSkipsWhatHoldsNone`, `TestAnEnvelopeBodyHoldsItsSealedTypes` |
| A type the App names is written as itself wherever it implements a sealed type, alike on every assembly; Sealed copies its variants | `TestSealedTypesWriteWhatTheAppNames`, `TestSealedKeepsItsVariants` |
| getatest checks every uncoded JSON body against its schema | `TestGetatestChecksBodiesAgainstTheDocument` (and every other test that uses getatest) |
| `Conforms` checks described problems, stream and upgrade refusals, and stream events | `TestConformsChecksDescribedFailures`, `TestConformsReadsEveryDescriptionOfAStatus`, `TestStreamAndUpgradeRefusalsAreCheckedAgainstTheDocument`, `TestConformsChecksStreamEvents` |
| `Conforms` checks each documented header with an author's schema | `TestConformsChecksDocumentedHeaders`, `TestConformsHoldsAMiddlewaresHeaderToItsType` |
| A response header is stated once, whatever its case | `TestAHeaderIsStatedOnceWhateverItsCase` |
| Declarations of one response header must agree on one schema | `TestMiddlewareHeaderSchemasAgree` |
| Body values are checked in tests, not at runtime; unwritable outputs and bad headers are runtime 500s | `TestGetatestChecksBodiesAgainstTheDocument`, `TestSealedTypeOutputDefects`, `TestOwnJSONOutputThatIsNotJSONIsADefect`, `TestOutputHeadersHoldToWhatAHeaderCarries` |
| The one-pass read accepts exactly what the reference path accepts | `FuzzSinglePass`, `FuzzSinglePassSealed`, `FuzzParamFastPath`, `TestSinglePassAgreesOnSealedTypes`, `TestSinglePassFindsLateDiscriminators`, `TestLateDiscriminatorLookAheadIsLinear`, `TestLateDiscriminatorLookAheadHoldsMaxDepth`, `TestSinglePassReadsOpaqueTypesWithTheBodyOptions`, `TestSinglePassHoldsMaxDepthInsideAnyJSON`, `fastdecode_test.go`, `fastdecode_edges_test.go` |
| The one-pass read sets a plain string, bool, integer, or float itself, as encoding/json/v2 would; a type with JSON or text methods, or one an unmarshaler in the options applies to, is read by v2; the strings it keeps are cleared when the decoder is released | `TestSinglePassSetsPlainLeavesItself`, `TestSinglePassYieldsToUnmarshalers`, `TestSinglePassAgreesOnPlainLeafBounds`, `TestInternKeepsStringsApart`, `TestReleaseClearsInternedStrings` |
| A body's work is linear in its size: a violation's path is rendered only when the violation is kept, `uniqueItems` compares elements by a keyed hash made once per element and confirms a match canonically, defaults at depth and nested sealed values cost no more per level | `TestBodyWorkIsLinear`, `TestViolationPathRendering`, `TestNestedUniqueItems`, `TestUniqueItemsHashCollision`, `BenchmarkLinearMapPaths`, `BenchmarkLinearUniqueReference`, `BenchmarkLinearUniqueDepth`, `BenchmarkLinearUniqueSinglePass` |
| Sealed values nested in one another are read in time linear in the body on both paths, the look-ahead's memo holds at most one span per 32 bytes, and a sealed type's unmarshaler reports the errors it reported reading each value whole and leaves no state behind | `TestLateDiscriminatorLookAheadIsLinear`, `TestLookAheadRecordsBoundedSpans`, `TestReferencePathReadsNestedSealedValuesInLinearTime`, `TestSealedReaderAgreesWithWhole`, `TestSealedReaderLeavesNoState`, `FuzzSealedReaderAgreesWithWhole` |
| Sealed bodies are read in one pass: 41 allocs/op against 131 | `BenchmarkPostSealed`, `BenchmarkPostSealedLate` |
| Output has the schema's shape: `[]`, `{}`, HTML-safe, sorted keys | `TestEncodeShapesMatchTheSchema`, `TestBodiesAreHTMLSafeAndNeverNull`, `TestProblemBodiesAreHTMLSafe`, `TestMapKeysAreWrittenSorted` |
| Output headers are written as text; a bodiless envelope writes headers alone | `TestScalarHeadersAndABodilessEnvelope` |
| A struct query parameter is a deepObject | `TestDeepObjectQueryParameters` |
| A value left out of a request is bound to its default | `TestDefaultsAreBoundWhereARequestLeavesThemOut`, `TestSinglePassAgreesOnKeywords`, `TestSinglePassAgreesOnSealedTypes`, `FuzzSinglePass`, `FuzzSinglePassSealed` |
| `geta.Nullable` tells absent, null, and a value apart | `TestNullableTellsAbsentFromNull`, `TestNullableSealedType`, `TestSinglePassAgreesOnKeywords`, `TestSinglePassAgreesOnNullableSealedTypes`, `FuzzSinglePass`, `FuzzSinglePassSealed` |
| Envelopes write headers, cookies, and a body; a failed envelope leaves none | `TestEnvelope`, `TestCookieAttributesRender`, `TestInvalidCookieIsADefect`, `TestRejectsInvalidCookieField`, `TestAFailedEnvelopeLeavesNoHeaders` |
| An envelope the document could not describe is refused | `TestEnvelopeHeaderNamesAreChecked`, `TestRejectsSetCookieHeaderField`, `TestRejectsSchemaTagOnOutputBody`, `TestOptionalEnvelopeBodyIsRefused` |
| An envelope's embedded struct fields are written by their own tags | `TestEnvelopeWritesEmbeddedFields`, `TestEnvelopeEmbeddedRefusals` |
| Under a ServeMux with the same parameter name, each side keeps its value | `TestMountedUnderTheSameParameterName` |
| The gate: secure by default, schemes as alternatives, 401/503/500 apart | `security_test.go`, `TestAlternativesDecideInOrderOfGravity` |
| Every 401 carries one challenge per refusing scheme | `TestEveryRefusingSchemeChallenges`, `TestAChallengerWithNoChallengeKeepsTheSchemes` |
| `Matched` follows a root rewrite from the next gate on | `TestMatchedFollowsARewriteFromTheNextGate`, `TestTwoRootGatesAroundARewrite`, `TestARedirectCarriesTheMatchOfItsCleanPath`, `TestBeforeGateFollowsARewriteBeforeTheGate`, `TestBeforeGateBesideAnOperationScopeGate`, `TestOptionsRunsNoBeforeGate` |
| Several gates must all admit | `TestTwoGatesEachRequireTheirDefault`, `TestSecurityRequirementsMatchTheGates` |
| Changing a Policy, a Doc, or a matched Doc after New changes no decision | `TestChangesAfterNewChangeNoDecision`, `TestSecureKeepsItsFlows` |
| Typed keys are distinct by identity; the zero key panics | `TestKeyRoundTrip` |

## Middleware, streams, upgrades (time in `testing/synctest`)

| Claim | Test |
| --- | --- |
| `ConcurrencyLimit` sheds with 503 and frees slots on return, panic, and stream start | `TestConcurrencyLimitShedsAndReleases`, `TestNestedConcurrencyLimitsFreeEverySlotForAStream`, `TestConcurrencyLimitIsDocumented` |
| `Timeout` is a context deadline; the 504 keeps CORS headers | `TestTimeoutIs504WithCORS`, `TestTimeoutLeavesAFastHandlerAlone`, `TestTimeoutKeepsAnEarlierDeadline` |
| `Timeout` bounds reading the body from its first read; a stall is a 408 | `TestASlowBodyIs408AndFreesItsSlot`, `TestALateBodyClosesOnlyAnHTTP1Connection`, `TestABodyInTimeLeavesTheConnectionAlone`, `TestABodyHasTheTimeoutsLengthFromItsFirstRead`, `TestTheBodyDeadlineIsDocumented`, `TestSendAStalledBodyGetsThe408` |
| `Doc.Timeout` sets the operation's deadline length | `TestDocTimeoutIsTheOperationsDeadline`, `TestDocTimeoutLengthensNestedTimeouts`, `TestDocTimeoutFollowsARootRewrite` |
| `Recover` turns panics into logged 500s, or aborts after the commit | `TestRecoverTurnsPanicsInto500`, `TestCORSHeadersOnARecovered500`, `TestRecoverAfterTheCommitAborts`, `TestRecoverAfterTheResponseStartedAborts`, `TestAPanicBehindABufferLeavesNoOutputHeaders` |
| CORS: wildcard, preflight, `Vary: Origin`, credentials | `TestCORSWildcard`, `TestCORSSpecificOriginVaries`, `TestCORSQueryPreflightBehindTheGate`, `TestCORSPreflightSpendsNoToken`, `TestCORSCompletesAResponseNothingWrote`, `TestCORSCompletesAFlushedResponse` |
| CORS exposes declared middleware and described-row headers, never `Set-Cookie` | `TestCORSExposesMiddlewareAndDescribedHeaders`, `TestCORSExposesABeforeGateRetryAfter`, `TestCORSUnmatched401ExposesOnlyTheListed` |
| A preflight allows the methods the path serves | `TestCORSPreflightAllowsThePathsMethods`, `TestCrossOriginConditionalWrite` (register) |
| CORS derives allowed and exposed headers from each operation | `TestCORSPreflightAllowsTheOperationsHeaders`, `TestCORSExposesTheOperationsHeaders`, `TestCORSConditionalWriteAcrossOrigins`, `TestCrossOriginConditionalWrite` (register) |
| The access log records the template and the status sent, never the raw path | `TestAccessLogRecordsTheTemplateNeverThePath`, `TestAccessLogRecordsWhatWasServed`, `TestAccessLogMarksAStream`, `TestAccessLogMarksAnUpgrade`, `TestAccessLogRecordsTheStatusSent` |
| Every log line records the method served, cut to 128 bytes | `TestAccessLogCutsALongMethod`, `TestRecoverLogsTheMethodServed` |
| geta's own client-error refusals are logged at Debug without request data | `TestGetasOwnRefusalsAreLoggedAtDebug` |
| ETag and Gzip: 304s, negotiation, threshold, media types, composition, framing | `TestETag`, `TestETagDeclaresItsNotModified`, `TestETagAndCheckReadIfNoneMatchAlike`, `TestETagKeepsTheHandlersTag`, `TestETagSkipsPost`, `TestETagLeavesAQueryUntagged`, `TestETagLeavesAnotherSuccessStatusAlone`, `TestETagListWithCommas`, `TestGzip`, `TestGzipMediaTypes`, `TestGzipQParameterIsCaseInsensitive`, `TestGzipOverETag`, `TestGzipFramingOnTheWire` |
| The gzip form has its own strong tag, read back as the handler's | `TestGzipTagsTheGzipForm`, `TestGzipFormTagMakesConditionalRequests`, `TestGzipLeavesIdentityTags`, `TestGzipLeavesWeakTags`, `TestGzipTagsReadBackUnambiguously`, `TestGzipStreamTagReadsBack`, `TestGzipSmallBodyKeepsTheTag`, `TestGzipTagsAResponseWrittenWithNothing`, `TestGzipVariesAnEmptyResponse` |
| ETag and Gzip give a streamed body its validator and compression | `TestLargeBodiesThroughGzipAndETag` |
| `geta.Compress` negotiates by RFC 9110 §12.5.3, never answering 406 | `TestCompressNegotiation`, `TestGzipHonoursIdentity`, `TestCompressRefusedIdentityCodesAnyBody`, `TestCompressOwnGzip`, `ExampleCompress` |
| `geta.Compress` leaves a 206 uncoded, its Content-Range counting uncoded bytes | `TestCompressLeavesAPartialResponseUncoded` |
| Each coded form has its own tag; weights are read by RFC 9110's grammar | `TestCompressNamesCodingsByTokens`, `TestCompressTagsEachCoding`, `TestCompressTagsReadBackUnambiguously`, `TestCompressLeavesWeakTags`, `TestCompressStreamPassesUncoded`, `TestCompressSmallBodyKeepsTheTag`, `TestCompressEncoderFailureSendsIdentity`, `TestCompressEncoderWriteFailureSendsIdentity`, `TestCompressHead`, `TestCompressReadsMalformedQValues` |
| getaotel names spans and metrics by route template; only a 5xx is an error | `TestAMatchedOperationIsNamedByItsTemplate`, `TestPathsOfOneTemplateShareASeries`, `TestAnUnmatchedRequestIsNamedByItsMethodAlone`, `TestARedirectIsNamedByTheTemplateItLeadsTo`, `TestAnIncomingTraceparentIsContinued`, `TestOnlyA5xxIsAnError` |
| getaotel records no raw path, no Host, and no X-Forwarded-For | `TestWhatTheClientChoosesIsNotRecorded` |
| getaotel records what was served, after rewrites and when mounted | `TestARewrittenRequestIsNamedByWhatWasServed`, `TestARewriteToNoOperationIsNamedByTheMethodServed`, `TestARewriteToNoOperationHasNoRoute`, `TestAHeadIsRecordedAsHead`, `TestAMountedAppTracesByTemplate`, `TestAMountedAppMeasuresByTemplate` |
| Streams and upgrades pass through getaotel | `TestAStreamFlushesThroughTheMiddleware`, `TestAnUpgradeSwitchesThroughTheMiddleware` |
| getaotel records aborted responses and uncaught panics as errors | `TestAnAbortedResponseIsRecorded`, `TestAPanicWithoutRecoverIsRecordedAndGoesOn`, `TestARewriteToAnUnknownMethodIsOther`, `TestWithNoOptionTheGlobalsAreUsed` |
| getaotel names geta's OPTIONS by its template | `TestOptionsIsNamedByItsTemplate` |
| getaotel records QUERY as QUERY, in spans and metrics | `TestAQueryIsNamedByItsTemplate` |
| Streams deliver events; event defects end the stream | `TestStreamDeliversEvents`, `TestStreamRefusesASplittingEventName`, `TestStreamEventDefectsEndTheStream`, `TestZeroStreamIsADefect`, `TestStreamSourcePanicAbortsTheConnection`, `TestHeadOnAStreamSendsHeadersOnly` |
| `KeepAlive`, `MaxIdle`, and `MaxLifetime` hold as stated | `TestKeepAliveDoesNotExtendMaxIdle`, `TestEventsRestartTheKeepAlive`, `TestEventsResetMaxIdleButNotMaxLifetime` |
| A stream is not bounded by `Timeout` and holds no slot | `TestTimeoutDoesNotBoundAStream`, `TestWaitingStreamFreesItsSlot` |
| Upgrades are authenticated before the switch; a plain request is 426 | `TestUnauthenticatedUpgradeIsRefusedBeforeTheSwitch`, `TestAPlainRequestToAnUpgradeIs426`, `TestAnUpgradeHeaderCannotNameTheProtocolAgain`, `TestAnUpgradeHeaderCannotRepeatTheDeprecation` |
| `Upgrade.Accept` hands the handshake to a WebSocket library through every geta writer | `TestUpgradeAcceptHandsTheHandshakeToALibrary`, `TestUpgradeAcceptIsNotCalledWithoutAnUpgradeRequest`, `TestUpgradeTokensAreReadAsTheGrammarWritesThem`, `TestUpgradeAcceptDefectsAndRefusals`, `TestServeClosesTheConnectionWhenItReturns`, `TestServeOnAConnectionThatCannotSwitchIsADefect` |
| Middleware writers unwrap for `http.ResponseController` | `TestMiddlewareWritersUnwrapForAResponseController` |
| getatest's `DialContext` reaches the app in memory | `TestGetatestDialContextReachesTheApp`, `TestPanelFeedOverWebSocket` (booking) |
| `Run` drains on shutdown with a grace period | `TestStreamsEndWhenTheServerDrains`, `TestRunStopsWhenItsContextEnds`, `TestRunStopsOnASignal`, `TestShutdownWaitsForAnInFlightRequest`, `TestShutdownGraceThenForcedClose`, `TestWithShutdownGraceSetsTheGrace`, `TestWithShutdownGraceRefusesANonPositiveGrace`, `TestServeClosesTheListener`, `TestRunKeepsTheCallersBaseContext`, `TestRunListensOnTheDefaultAddress`, `TestRunReportsAListenFailure` |
| `Run` bounds slow headers and idle connections, not streams | `TestRunBoundsSlowHeadersAndIdleConnections`, `TestRunLimitsDoNotCutAStream` |
| A server with a TLS certificate serves HTTP/2 over TLS | `TestServeNegotiatesHTTP2OverTLS`, `TestRunServesTLS`, `TestServeTLSWithGetConfigForClient` |

## The document

| Claim | Test |
| --- | --- |
| The document lists every status an operation can answer, with every cause | `TestMiddlewareAnswersAnotherSuccessWithoutContent`, `TestMiddlewareAnswers304OnlyWhereItCanBe`, `TestConformsChecksAMiddlewaresRedirect`, `TestDocumentShape`, `TestAuthorizationIsOrdinaryMiddleware`, `TestSpecialOutputsAreDocumented`, `TestETagDeclaresItsNotModified`, `TestRowAndGetaOnOneStatusAreBothDocumented`, `TestRowAndMiddlewareOnOneStatusAreBothDocumented`, `TestARowBesideABeforeGateAnswerIsDocumentedWithBoth` |
| `Deprecation` and `Sunset` are declared on every response | `TestDeprecationIsDocumentedOnEveryResponse` |
| A middleware's `Header` is documented on the status it answers | `TestMiddlewareHeadersAreDocumented` |
| `geta.HeaderOf` types a middleware's header; tests hold it to the type | `TestMiddlewareHeaderTypesAreDocumented`, `TestMiddlewareHeaderTypeMistakesAreRefused`, `TestConformsHoldsAMiddlewaresHeaderToItsType` |
| A described row's status is documented with its problem | `TestDescribedFailuresAreDocumented`, `TestOneDescriptionTypeTwiceOnAStatus`, `TestAHeadersOnlyDescriptionIsTheProblem`, `TestDescriptionsAreNoComponentsOfTheirOwn`, `TestEveryComponentIsReferredTo` |
| A `Doc.Scope`'s answers are documented on its operation alone | `TestOperationScopeAnswersAreDocumentedOnItsOperation` |
| A `Doc.BeforeGate`'s answers are documented on its operation alone | `TestBeforeGateAnswersAreDocumentedWhereTheyApply`, `TestLoginIsRateLimited` (auth) |
| `geta.Conditional` documents its headers and its 412, 304, and 428 | `TestConditionalIsDocumented`, `TestConditionalStatusesAreDocumentedOnTheWire` |
| getatest fails on an undocumented status | `TestGetatestFailsOnAnUndocumentedStatus`, `TestGetatestChecksTheOperationServed`, `TestGetatestNamesTheOperationServed` (and every other test that uses getatest) |
| getatest's header setters replace, never duplicate | `TestClientWithReplaces`, `TestSendPrefersTheRequestsOwnHeader`, `TestStreamAndUpgradeSendTheClientsHeadersOnce` |
| The document does not depend on table order | `TestDocumentIgnoresTableOrder` |
| Every path has a documented `options` operation | `TestOptionsIsDocumented`, `TestOptionsUnionsTheBranches`, `TestOptionsRunsTheRootScopeOnly`, `TestOptionsIsAnOperation`, `TestOptionsDefectIsDocumented` |
| A deepObject is documented as OpenAPI defines one | `TestDeepObjectIsDocumented`, `TestDeepObjectMistakesAreRefused` |
| `doc`, `default`, `examples`, and `deprecated` go into the document | `TestDocAndAnnotationsAreDocumented` |
| `geta.Nullable` is documented as its schema or null | `TestNullableIsDocumentedAsTypeOrNull` |
| Every declared success status is documented | `TestEveryDeclaredStatusIsDocumented` |
| A raw body is documented as its media type with no schema | `TestRawBodiesAreDocumented` |
| Form and multipart bodies are documented as closed objects; every body operation documents its 415 | `TestFormDocument`, `TestUnsupportedMediaTypeIs415`, `TestContentCodingIs415` |
| The document is OpenAPI 3.1.0, or 3.2.0 with `WithOpenAPI(OpenAPI32)` | `TestOpenAPIDefaultsTo31`, `TestOpenAPI32DescribesEventsWithItemSchema`, `TestOpenAPI32GivesResponsesASummary`, `TestOpenAPI32NamesTheCookiesSet`, `TestQueryNeedsOpenAPI32`, `TestDocument32` (auth) |
| The documents pass their version's OpenAPI meta-schema, the goldens of `TestSchemaKeywordsGolden` and `TestResponsesGolden` included | `uvx openapi-spec-validator examples/register/openapi.json examples/auth/openapi.json examples/auth/openapi.3.2.json examples/booking/openapi.json examples/booking/openapi.3.2.json testdata/formats.openapi.json testdata/forms.openapi.json testdata/query.openapi.3.2.json testdata/schema.openapi.json testdata/responses.openapi.json testdata/security.openapi.json testdata/security.openapi.3.2.json examples/bookmarks/openapi.json` |
| A generated Python client round-trips with each example | `examples/register/clientcheck/roundtrip.py`, `examples/auth/clientcheck/roundtrip.py`, `examples/booking/clientcheck/roundtrip.py` (`openapi-python-client`) |
| getaclient round-trips with the handlers' own types | `TestTypedClientRoundTrip` (register), `TestTypedClientCoversEveryLocation`, `TestTypedClientReturnsTheProblem`, `TestTypedClientChecksTheCallAgainstTheApp` |
| getaclient sends and reads raw bodies | `TestTypedClientCarriesRawBodies` |
| `getaclient.Absent` leaves fields out so their defaults bind | `TestAbsentFieldsTakeTheirDefaults`, `TestAbsentRefusesWhatHasNoDefault`, `TestAbsentKeepsTheClientsJSONOptions` |
| getaclient reads declared 3xx as success and follows no redirect | `TestDeclaredRedirectsAreSuccesses`, `TestMiddlewareSuccessesAreNoOutput`, `TestASuccessOfAnotherMediaTypeIsAnError`, `TestProblemAsReadsSealedTypes`, `TestClientReadsHeadersOfEveryType`, `TestClientErrorText` |
| getaclient reads a response body up to its limit | `TestAResponseBodyIsReadUpToTheLimit` |
| `getaclient.ResponseHeader` exposes the response header | `TestResponseHeaderReadsADeprecatedSuccess`, `TestResponseHeaderRefusesANilPointer`, `TestDeprecationReadsTheHeadersForms` |
| getaclient escapes paths and refuses values a header or cookie cannot carry | `TestTypedClientSendsDotValuesAsValues`, `TestTypedClientEscapesLiteralSegments`, `TestClientReadsEnvelopeEmbeddedFields`, `TestClientCallHeaderWinsOverDefault`, `TestClientRefusesCookieValuesACookieCannotCarry`, `TestClientRefusesAHeaderValueAHeaderCannotCarry`, `TestClientRefusesAHeaderListElementTheListCannotCarry` |
| A changed document is an ordinary test failure | `TestOpenAPIGolden` (register), `TestDocumentCarriesTheDeclarations` and `TestDocument32` (auth), `TestFormatTypesInTheDocument`, `TestFormDocument` |

## The table

| Claim | Test |
| --- | --- |
| Directories map to URLs; `id_` is `{id}`; scopes apply to their subtree | `TestURLsFollowDirectories` |
| The root package is named after its directory, always validly | `TestPackageNameFollowsTheDirectory`, `TestTheRootPackageIsNamedAfterItsDirectory` |
| The generated table compiles and serves | `TestSyncedTableBuilds` |
| The output is a pure function of the tree | `TestRenderIsAPureFunctionOfTheTree` |
| `sync -check` and `check` name what is out of sync | `TestSyncCheckAndCheckNameTheForgottenDirectory`, `TestCheckNamesAScopeGuardingNothing` |
| `go test` runs every assembly check through `zz_routes_test.go` | `TestGeneratedTestRunsTheAssemblyChecks` |
| `-update` rewrites goldens; the commands exit 1 on findings and 2 on usage errors | `TestUpdateReachesEveryGoldenOfTheModule`, `TestGoldenUpdatesAndCompares`, `TestSyncAndCheckExitCodes`, `TestUsageErrorsExit2` |
| The root's `options.go` is passed to every assembly, the generated test's included | `TestGeneratedTestAssemblesWithTheRootOptions`, `TestCheckNamesOptionsTheTestDoesNotPass`, `TestSameAppAsTheRegisterExample` (examples/register-sql) |
| getavet reports what source decides, with `geta.New`'s text, and leaves the rest to `geta.New` | `getavet` tests (`TestStreamEventsMatchGetaNew`, `TestPartiallyExplicitProblemsMatchGetaNew`, `TestWhatGetaNewAloneDecides`, `TestEachMistakeIsNamed`, `TestSoundTreePasses`, `TestSchemaTagOnEmbeddedStruct`, `TestEnvelopeAndInputFieldRefusals`, `TestInputEmbeddedAndEnvelopeBody`, `TestTypeAndEmbeddedRefusals`, `TestMemberAndTextRefusalsMatchGetaNew`, `TestGenericTypeNamesMatchGetaNew`, `TestOneSidedKeysAndUncheckedFormatsMatchGetaNew`, `TestSchemaTagsOnObjectsMatchGetaNew`, `TestMapKeysAreJudgedForEveryReceiver`, `TestKeyFormatsMatchGetaNew`, `TestFormatTypes`, `TestFormatTypesMatchGetaNew`, `TestFormatOnAFormatTypeMatchesGetaNew`, `TestUncarriedValues`, `TestLowerBoundsPastTheBackstop`, `TestEnumMembersPastTheBackstop`, `TestNullableMatchesGetaNew`, `TestDefaultsAndDocsMatchGetaNew`, `TestDeepObjectsMatchGetaNew`, `TestSelfHoldingTypesInAnyOrderMatchGetaNew`), `TestCheckSchemaTag`, `TestUnexportedMembersAndOutputHeaders`, `TestAFormatTypeTheDocumentCannotTrustIsRefused` |
| Routes in directories `./...` skips, bad parameter names, and alias collisions are refused | `TestRejectsDirectoriesTheGoCommandIgnores`, `TestRejectsARootTheGoCommandIgnores`, `TestRejectsBadParameterNames`, `TestRejectsAliasCollision`, `TestRejectsScopeAliasCollision`, `TestParameterNamesFollowServeMux` |

## The examples

What the examples show is application code, not geta's: an application copies it or writes its own.

| Claim | Test |
| --- | --- |
| register-sql: the conformance suite (`examples/register-sql/conformance`) checks a classifier; the example adapters pass it | `TestAConformantAdapterPasses`, `TestANonConformantAdapterFails`, `examples/register-sql/sqlite TestConformance`, `examples/register-sql/postgres TestConformance` |
| register-sql: constraint violations are `store.ErrConflict`, serialization failures `store.ErrTransient`, unreachable `store.ErrUnavailable` | conformance suite, `TestForeignKeyIsAConflict`, `TestSerializationFailureIsTransient`, `TestUnreachableIsUnavailable`, `TestUnopenableIsUnavailable` |
| register: geta maps no database error itself; the application's rows do | `TestUnavailableStoreIs503`, `TestTransientStoreIs503` (register) |
| register: the routes run unchanged on memory, SQLite, and PostgreSQL | `TestFlowOnTheMemoryStore`, `examples/register-sql/{sqlite,postgres} TestRegisterExample`, `TestRoutesImportNoDriver` |
| booking: `jwtauth` allows only asymmetric algorithms and requires exp, issuer, and audience | `examples/booking/jwtauth/jwt_test.go`, `TestDefaultAlgorithms`, `TestIssuedAtIsChecked` |
| booking: `jwtauth.Verifier` refuses a misconfigured Validator before serving | `TestAMisconfiguredValidatorIsRefused` |
| booking: `jwtauth.RequireScopes` runs at `OrderAuthorize` and answers 403 | `TestRequireScopesAuthorizes`, `TestRequireScopesForAnotherScheme`, `TestBearerBesideAnotherScheme` |
| booking: `jwtauth.RequireScopes` states its scopes in the document | `TestRequireScopesAreDocumented`, `TestRequireScopesForAnotherScheme`, `TestRequireScopes` |
| booking: `jwtauth` fetches no keys; no keys is 503, an unknown key 401 | `TestNoKeysHeldIsUnavailable`, `TestKeyTypeMustMatchAlg`, `ExampleVerifier` |
| booking: bare challenge, `invalid_token`, 503 with no key, `insufficient_scope` | `examples/booking/jwtauth/verifier_test.go` |
| register: CRUD, secure by default, roles, the event feed | `examples/register/register_test.go` |
| register: user writes are administrator-only through `Doc.Scope` | `TestUserWritesAreAdminOnly`, `TestNotAdminIsRefusedBeforeTheContract`, `TestAdminOnlyIsDocumentedWhereItApplies`, `registertest.Run` |
| register: roles change through one route; the last admin is kept | `TestRolesChangeOnlyThroughTheAdminRoute`, `TestCRUD`, `registertest.Run`, `examples/register-sql/postgres TestLastAdminUnderConcurrentDemotions`, `TestLastAdminUnderConcurrentDeletions` |
| register: `ETag` and `If-Match` prevent lost updates on every store | `registertest.Run` (memory, SQLite, PostgreSQL), `TestTypedClientRoundTrip` (register), `TestCrossOriginConditionalWrite` |
| register: failure tables, shared rows, all-or-none import, teams | `TestTeams`, `registertest.Run` (memory, SQLite, PostgreSQL) |
| register: problem types tell rows apart; what geta answers itself | `TestProblemTypesTellRowsApart`, `TestWhatGetaAnswers` (register) |
| auth: bearer and cookie sessions, 401 vs 403, revocation, a rate-limited form login | `examples/auth/auth_test.go` |
| booking: ES256 tokens via keyfunc, `RequireScopes`, 503 without keys | `TestBookAndChange`, `TestNoKeysIsUnavailable`, `TestForeignTokens` |
| booking: sealed changes, format types, own-JSON types, typed problems, typed client | `TestBookAndChange`, `TestTypedClientRoundTrip` (booking) |
| booking: zstd, getaotel, WebSocket, rate limit, `Limits`, HTTP/2 and HTTP/3 | `TestCompressedListsAndTheirTags`, `TestTracesAndMetrics`, `TestPanelFeedOverWebSocket`, `TestRateLimit`, `TestWhatGetaAnswers` (booking), `TestHTTP2AndHTTP3` |
