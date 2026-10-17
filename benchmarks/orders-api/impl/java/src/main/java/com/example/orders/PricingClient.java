package com.example.orders;

import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;
import jakarta.annotation.PreDestroy;
import java.io.IOException;
import java.net.URI;
import java.net.URLEncoder;
import java.net.http.HttpClient;
import java.net.http.HttpRequest;
import java.net.http.HttpResponse;
import java.nio.charset.StandardCharsets;
import java.time.Duration;
import java.util.List;
import java.util.concurrent.CompletionService;
import java.util.concurrent.ExecutionException;
import java.util.concurrent.ExecutorCompletionService;
import java.util.concurrent.ExecutorService;
import java.util.concurrent.Executors;
import java.util.concurrent.Future;
import java.util.concurrent.Semaphore;
import java.util.concurrent.TimeUnit;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;
import org.springframework.stereotype.Component;

/**
 * Client for the external pricing service.
 *
 * <p>Protections against a slow or failing dependency:
 * <ul>
 *   <li>connect timeout and per-request timeout;</li>
 *   <li>an overall deadline per quote, after which the caller gets 503;</li>
 *   <li>a process-wide bulkhead capping in-flight pricing calls;</li>
 *   <li>a per-quote concurrency cap so one large order cannot take the whole bulkhead.</li>
 * </ul>
 */
@Component
public class PricingClient {

    private static final Logger log = LoggerFactory.getLogger(PricingClient.class);

    private final HttpClient http;
    private final ObjectMapper json;
    private final String baseUrl;
    private final Duration requestTimeout;
    private final Duration quoteDeadline;
    private final Semaphore inFlight;
    private final int perQuoteConcurrency;

    public PricingClient(OrdersProperties props, ObjectMapper json) {
        OrdersProperties.Pricing p = props.pricing();
        if (p.url() == null || p.url().isBlank()) {
            throw new IllegalArgumentException("PRICING_URL is not set");
        }
        URI base = URI.create(p.url());
        if (base.getScheme() == null || base.getHost() == null) {
            throw new IllegalArgumentException("PRICING_URL must be an absolute http(s) URL");
        }
        this.baseUrl = p.url().replaceAll("/+$", "");
        this.json = json;
        this.requestTimeout = p.requestTimeout();
        this.quoteDeadline = p.quoteDeadline();
        this.inFlight = new Semaphore(p.maxInFlight());
        this.perQuoteConcurrency = p.perQuoteConcurrency();
        this.http = HttpClient.newBuilder()
                .version(HttpClient.Version.HTTP_1_1)
                .connectTimeout(p.connectTimeout())
                .followRedirects(HttpClient.Redirect.NEVER)
                .build();
    }

    /**
     * Sum of price x qty over all items.
     *
     * @throws ApiException 422 if any SKU is unknown, 503 if pricing fails or exceeds the deadline
     */
    public long totalCents(List<Order.Item> items) {
        if (items.isEmpty()) {
            return 0;
        }
        long deadline = System.nanoTime() + quoteDeadline.toNanos();
        Semaphore perQuote = new Semaphore(perQuoteConcurrency);
        // Not try-with-resources: close() would wait for stragglers; we want to abandon them.
        ExecutorService executor = Executors.newVirtualThreadPerTaskExecutor();
        try {
            CompletionService<Long> results = new ExecutorCompletionService<>(executor);
            for (Order.Item item : items) {
                results.submit(() -> lineTotal(item, perQuote, deadline));
            }
            long total = 0;
            for (int i = 0; i < items.size(); i++) {
                long remaining = deadline - System.nanoTime();
                Future<Long> done = remaining > 0 ? results.poll(remaining, TimeUnit.NANOSECONDS) : null;
                if (done == null) {
                    log.warn("pricing quote exceeded deadline of {}", quoteDeadline);
                    throw ApiException.unavailable("pricing service timed out");
                }
                total = Math.addExact(total, done.get());
            }
            return total;
        } catch (ExecutionException e) {
            if (e.getCause() instanceof ApiException api) {
                throw api;
            }
            log.warn("pricing request failed: {}", e.getCause().toString());
            throw ApiException.unavailable("pricing service unavailable");
        } catch (InterruptedException e) {
            Thread.currentThread().interrupt();
            throw ApiException.unavailable("pricing service unavailable");
        } finally {
            executor.shutdownNow();
        }
    }

    private long lineTotal(Order.Item item, Semaphore perQuote, long deadline) throws IOException, InterruptedException {
        if (!perQuote.tryAcquire(remainingNanos(deadline), TimeUnit.NANOSECONDS)) {
            throw ApiException.unavailable("pricing service timed out");
        }
        try {
            if (!inFlight.tryAcquire(remainingNanos(deadline), TimeUnit.NANOSECONDS)) {
                log.warn("pricing bulkhead saturated");
                throw ApiException.unavailable("pricing service overloaded");
            }
            try {
                return Math.multiplyExact(fetchCents(item.sku(), deadline), (long) item.qty());
            } finally {
                inFlight.release();
            }
        } finally {
            perQuote.release();
        }
    }

    private long fetchCents(String sku, long deadline) throws IOException, InterruptedException {
        long remaining = remainingNanos(deadline);
        if (remaining <= 0) {
            throw ApiException.unavailable("pricing service timed out");
        }
        Duration timeout = Duration.ofNanos(Math.min(remaining, requestTimeout.toNanos()));
        String encodedSku = URLEncoder.encode(sku, StandardCharsets.UTF_8).replace("+", "%20");
        HttpRequest request = HttpRequest.newBuilder(URI.create(baseUrl + "/prices/" + encodedSku))
                .timeout(timeout)
                .header("Accept", "application/json")
                .GET()
                .build();

        HttpResponse<byte[]> response = http.send(request, HttpResponse.BodyHandlers.ofByteArray());
        int status = response.statusCode();
        if (status == 404) {
            throw ApiException.unprocessable("unknown sku: " + sku);
        }
        if (status != 200) {
            throw new IOException("pricing service returned HTTP " + status);
        }
        JsonNode cents = json.readTree(response.body()).path("cents");
        if (!cents.isIntegralNumber() || !cents.canConvertToLong() || cents.asLong() < 0) {
            throw new IOException("pricing service returned an invalid price for sku " + sku);
        }
        return cents.asLong();
    }

    private static long remainingNanos(long deadline) {
        return Math.max(0, deadline - System.nanoTime());
    }

    @PreDestroy
    void close() {
        http.shutdownNow();
    }
}
