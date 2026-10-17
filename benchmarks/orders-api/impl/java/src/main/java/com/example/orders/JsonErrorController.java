package com.example.orders;

import jakarta.servlet.RequestDispatcher;
import jakarta.servlet.http.HttpServletRequest;
import org.springframework.boot.web.servlet.error.ErrorController;
import org.springframework.http.HttpStatusCode;
import org.springframework.http.MediaType;
import org.springframework.http.ResponseEntity;
import org.springframework.web.bind.annotation.RequestMapping;
import org.springframework.web.bind.annotation.RestController;

/** Errors raised outside Spring MVC (container level) also use the {"error": ...} shape. */
@RestController
public class JsonErrorController implements ErrorController {

    @RequestMapping("${server.error.path:/error}")
    public ResponseEntity<Api.ErrorBody> error(HttpServletRequest request) {
        int status = request.getAttribute(RequestDispatcher.ERROR_STATUS_CODE) instanceof Integer s ? s : 500;
        HttpStatusCode code = HttpStatusCode.valueOf(status);
        return ResponseEntity.status(code)
                .contentType(MediaType.APPLICATION_JSON)
                .body(new Api.ErrorBody(ApiExceptionHandler.reason(code)));
    }
}
