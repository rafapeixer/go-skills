package com.example.orders;

import java.net.URI;
import java.util.ArrayList;
import java.util.HashSet;
import java.util.List;
import java.util.Map;
import java.util.Set;
import org.springframework.http.ResponseEntity;
import org.springframework.web.bind.annotation.GetMapping;
import org.springframework.web.bind.annotation.PathVariable;
import org.springframework.web.bind.annotation.PostMapping;
import org.springframework.web.bind.annotation.RequestBody;
import org.springframework.web.bind.annotation.RequestParam;
import org.springframework.web.bind.annotation.RestController;

@RestController
public class OrderController {

    private static final int MAX_ITEMS = 100;
    private static final int MAX_SKU_LENGTH = 255;
    private static final int DEFAULT_LIMIT = 20;
    private static final int MAX_LIMIT = 100;

    private final OrderRepository orders;
    private final PricingClient pricing;

    public OrderController(OrderRepository orders, PricingClient pricing) {
        this.orders = orders;
        this.pricing = pricing;
    }

    @GetMapping("/healthz")
    public Map<String, String> health() {
        // Liveness only: do not fail every replica at once because the database blipped.
        return Map.of("status", "ok");
    }

    @PostMapping("/orders")
    public ResponseEntity<Api.CreatedResponse> create(@RequestBody(required = false) Api.CreateOrderRequest request) {
        if (request == null) {
            throw ApiException.badRequest("request body is required");
        }
        if (request.customerId() == null || request.customerId() <= 0) {
            throw ApiException.badRequest("customer_id must be a positive integer");
        }
        List<Order.Item> items = validateItems(request.items());
        long id = orders.create(request.customerId(), items);
        return ResponseEntity.created(URI.create("/orders/" + id)).body(new Api.CreatedResponse(id));
    }

    @GetMapping("/orders/{id}")
    public Api.OrderView get(@PathVariable long id) {
        return orders.find(id)
                .map(Api.OrderView::of)
                .orElseThrow(() -> ApiException.notFound("order not found"));
    }

    @GetMapping("/customers/{customerId}/orders")
    public Api.OrderPage listByCustomer(
            @PathVariable long customerId,
            @RequestParam(required = false) String limit,
            @RequestParam(required = false) String cursor) {
        if (customerId <= 0) {
            throw ApiException.badRequest("customer id must be a positive integer");
        }
        int pageSize = parseLimit(limit);
        Cursor after = cursor == null || cursor.isEmpty() ? null : Cursor.decode(cursor);

        // Fetch one extra row to learn whether another page exists.
        List<Order> page = orders.findByCustomer(customerId, after, pageSize + 1);
        String nextCursor = "";
        if (page.size() > pageSize) {
            page = page.subList(0, pageSize);
            Order last = page.getLast();
            nextCursor = new Cursor(last.createdAt(), last.id()).encode();
        }
        return new Api.OrderPage(page.stream().map(Api.OrderView::of).toList(), nextCursor);
    }

    @GetMapping("/orders/{id}/quote")
    public Api.QuoteResponse quote(@PathVariable long id) {
        // The DB connection is released before calling the pricing service.
        Order order = orders.find(id).orElseThrow(() -> ApiException.notFound("order not found"));
        return new Api.QuoteResponse(order.id(), pricing.totalCents(order.items()));
    }

    private static List<Order.Item> validateItems(List<Api.ItemRequest> items) {
        if (items == null || items.isEmpty() || items.size() > MAX_ITEMS) {
            throw ApiException.badRequest("items must contain between 1 and " + MAX_ITEMS + " entries");
        }
        Set<String> seen = new HashSet<>();
        List<Order.Item> result = new ArrayList<>(items.size());
        for (Api.ItemRequest item : items) {
            if (item == null) {
                throw ApiException.badRequest("items must not contain null");
            }
            String sku = item.sku();
            if (sku == null || sku.isBlank()) {
                throw ApiException.badRequest("sku is required");
            }
            if (sku.length() > MAX_SKU_LENGTH || sku.chars().anyMatch(Character::isISOControl)) {
                throw ApiException.badRequest("invalid sku");
            }
            if (item.qty() == null || item.qty() < 1) {
                throw ApiException.badRequest("qty must be an integer >= 1");
            }
            if (!seen.add(sku)) {
                throw ApiException.badRequest("duplicate sku: " + sku);
            }
            result.add(new Order.Item(sku, item.qty()));
        }
        return result;
    }

    private static int parseLimit(String limit) {
        if (limit == null || limit.isEmpty()) {
            return DEFAULT_LIMIT;
        }
        int value;
        try {
            value = Integer.parseInt(limit);
        } catch (NumberFormatException e) {
            throw ApiException.badRequest("limit must be an integer");
        }
        if (value < 1) {
            throw ApiException.badRequest("limit must be >= 1");
        }
        return Math.min(value, MAX_LIMIT);
    }
}
