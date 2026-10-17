package com.example.orders;

import com.zaxxer.hikari.HikariConfig;
import com.zaxxer.hikari.HikariDataSource;
import java.net.URI;
import java.net.URLDecoder;
import java.nio.charset.StandardCharsets;
import java.time.Duration;
import org.springframework.context.annotation.Bean;
import org.springframework.context.annotation.Configuration;

@Configuration(proxyBeanMethods = false)
public class DataSourceConfig {

    @Bean(destroyMethod = "close")
    public HikariDataSource dataSource(OrdersProperties props) {
        OrdersProperties.Db db = props.db();
        PostgresUrl url = PostgresUrl.parse(props.databaseUrl());

        HikariConfig config = new HikariConfig();
        config.setPoolName("orders-db");
        config.setJdbcUrl(url.jdbcUrl());
        if (url.username() != null) {
            config.setUsername(url.username());
        }
        if (url.password() != null) {
            config.setPassword(url.password());
        }
        // Fixed-size pool: the connection budget is shared by all replicas.
        config.setMaximumPoolSize(db.poolSize());
        config.setMinimumIdle(db.poolSize());
        // Fail fast (-> 503) instead of queueing virtual threads indefinitely.
        config.setConnectionTimeout(db.connectionTimeout().toMillis());
        config.setValidationTimeout(1_000);
        config.setMaxLifetime(Duration.ofMinutes(30).toMillis());
        config.setKeepaliveTime(Duration.ofMinutes(2).toMillis());

        config.addDataSourceProperty("ApplicationName", "orders-api");
        config.addDataSourceProperty("options", "-c statement_timeout=" + db.statementTimeout().toMillis());
        config.addDataSourceProperty("connectTimeout", "5");
        config.addDataSourceProperty("socketTimeout", "30");
        config.addDataSourceProperty("tcpKeepAlive", "true");
        return new HikariDataSource(config);
    }

    /** Converts {@code postgres://user:pass@host:port/db?params} into a JDBC URL plus credentials. */
    record PostgresUrl(String jdbcUrl, String username, String password) {

        static PostgresUrl parse(String raw) {
            if (raw == null || raw.isBlank()) {
                throw new IllegalArgumentException("DATABASE_URL is not set");
            }
            if (raw.startsWith("jdbc:postgresql:")) {
                return new PostgresUrl(raw, null, null);
            }
            URI uri;
            try {
                uri = URI.create(raw);
            } catch (IllegalArgumentException e) {
                // Do not echo the URL: it contains credentials.
                throw new IllegalArgumentException("DATABASE_URL is not a valid URL");
            }
            String scheme = uri.getScheme();
            if (!"postgres".equals(scheme) && !"postgresql".equals(scheme)) {
                throw new IllegalArgumentException("DATABASE_URL must use the postgres:// or postgresql:// scheme");
            }
            if (uri.getHost() == null) {
                throw new IllegalArgumentException("DATABASE_URL has no host");
            }
            String path = uri.getRawPath() == null ? "" : uri.getRawPath().replaceFirst("^/", "");
            if (path.isEmpty()) {
                throw new IllegalArgumentException("DATABASE_URL has no database name");
            }
            int port = uri.getPort() == -1 ? 5432 : uri.getPort();
            StringBuilder jdbc = new StringBuilder("jdbc:postgresql://")
                    .append(uri.getHost()).append(':').append(port).append('/').append(path);
            if (uri.getRawQuery() != null) {
                jdbc.append('?').append(uri.getRawQuery());
            }

            String user = null;
            String password = null;
            String userInfo = uri.getRawUserInfo();
            if (userInfo != null) {
                int colon = userInfo.indexOf(':');
                user = decode(colon < 0 ? userInfo : userInfo.substring(0, colon));
                password = colon < 0 ? null : decode(userInfo.substring(colon + 1));
            }
            return new PostgresUrl(jdbc.toString(), user, password);
        }

        private static String decode(String s) {
            // Percent-decoding only; '+' is literal in URL user info.
            return URLDecoder.decode(s.replace("+", "%2B"), StandardCharsets.UTF_8);
        }
    }
}
