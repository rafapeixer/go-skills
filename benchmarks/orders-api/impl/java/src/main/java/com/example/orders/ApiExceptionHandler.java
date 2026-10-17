package com.example.orders;

import org.slf4j.Logger;
import org.slf4j.LoggerFactory;
import org.springframework.dao.DataAccessResourceFailureException;
import org.springframework.dao.TransientDataAccessException;
import org.springframework.http.HttpHeaders;
import org.springframework.http.HttpStatus;
import org.springframework.http.HttpStatusCode;
import org.springframework.http.MediaType;
import org.springframework.http.ProblemDetail;
import org.springframework.http.ResponseEntity;
import org.springframework.web.bind.annotation.ExceptionHandler;
import org.springframework.web.bind.annotation.RestControllerAdvice;
import org.springframework.web.context.request.WebRequest;
import org.springframework.web.servlet.mvc.method.annotation.ResponseEntityExceptionHandler;

/** Renders every error as {@code {"error":"<message>"}}. */
@RestControllerAdvice
public class ApiExceptionHandler extends ResponseEntityExceptionHandler {

    private static final Logger log = LoggerFactory.getLogger(ApiExceptionHandler.class);

    @ExceptionHandler(ApiException.class)
    public ResponseEntity<Api.ErrorBody> handleApi(ApiException e) {
        return error(e.status(), e.getMessage());
    }

    /** Pool exhausted, connection refused, statement timeout, serialization failure, ... */
    @ExceptionHandler({DataAccessResourceFailureException.class, TransientDataAccessException.class})
    public ResponseEntity<Api.ErrorBody> handleDatabaseUnavailable(Exception e) {
        log.warn("database unavailable: {}", e.toString());
        return error(HttpStatus.SERVICE_UNAVAILABLE, "database unavailable");
    }

    @ExceptionHandler(Exception.class)
    public ResponseEntity<Api.ErrorBody> handleUnexpected(Exception e) {
        log.error("unhandled exception", e);
        return error(HttpStatus.INTERNAL_SERVER_ERROR, "internal error");
    }

    /** Standard Spring MVC exceptions (malformed JSON, 404 route, 405, 415, type mismatch, ...). */
    @Override
    protected ResponseEntity<Object> handleExceptionInternal(
            Exception ex, Object body, HttpHeaders headers, HttpStatusCode statusCode, WebRequest request) {
        String message = reason(statusCode);
        if (statusCode.value() == 400 && body instanceof ProblemDetail pd && pd.getDetail() != null) {
            message = pd.getDetail();
        }
        if (statusCode.is5xxServerError()) {
            log.error("request failed", ex);
        }
        return ResponseEntity.status(statusCode)
                .headers(headers)
                .contentType(MediaType.APPLICATION_JSON)
                .body(new Api.ErrorBody(message));
    }

    static String reason(HttpStatusCode code) {
        HttpStatus status = HttpStatus.resolve(code.value());
        return status == null ? "error" : status.getReasonPhrase().toLowerCase();
    }

    private static ResponseEntity<Api.ErrorBody> error(HttpStatus status, String message) {
        return ResponseEntity.status(status).contentType(MediaType.APPLICATION_JSON).body(new Api.ErrorBody(message));
    }
}
