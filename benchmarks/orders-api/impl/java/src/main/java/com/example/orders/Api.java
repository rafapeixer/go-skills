package com.example.orders;

import com.fasterxml.jackson.annotation.JsonProperty;
import java.time.ZoneOffset;
import java.time.format.DateTimeFormatter;
import java.util.List;

/** JSON request and response bodies. */
public final class Api {

    private Api() {
    }

    public record CreateOrderRequest(
            @JsonProperty("customer_id") Long customerId,
            List<ItemRequest> items) {
    }

    public record ItemRequest(String sku, Integer qty) {
    }

    public record CreatedResponse(long id) {
    }

    public record ItemView(String sku, int qty) {
    }

    public record OrderView(
            long id,
            @JsonProperty("customer_id") long customerId,
            String status,
            @JsonProperty("created_at") String createdAt,
            List<ItemView> items) {

        static OrderView of(Order o) {
            return new OrderView(
                    o.id(),
                    o.customerId(),
                    o.status(),
                    o.createdAt().withOffsetSameInstant(ZoneOffset.UTC).format(DateTimeFormatter.ISO_OFFSET_DATE_TIME),
                    o.items().stream().map(i -> new ItemView(i.sku(), i.qty())).toList());
        }
    }

    public record OrderPage(
            List<OrderView> orders,
            @JsonProperty("next_cursor") String nextCursor) {
    }

    public record QuoteResponse(
            @JsonProperty("order_id") long orderId,
            @JsonProperty("total_cents") long totalCents) {
    }

    public record ErrorBody(String error) {
    }
}
