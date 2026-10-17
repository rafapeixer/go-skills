package com.example.orders;

import java.time.Duration;
import org.springframework.boot.context.properties.ConfigurationProperties;

@ConfigurationProperties("orders")
public record OrdersProperties(String databaseUrl, Db db, Pricing pricing) {

    public record Db(int poolSize, Duration connectionTimeout, Duration statementTimeout) {
    }

    public record Pricing(
            String url,
            Duration connectTimeout,
            Duration requestTimeout,
            Duration quoteDeadline,
            int maxInFlight,
            int perQuoteConcurrency) {
    }
}
