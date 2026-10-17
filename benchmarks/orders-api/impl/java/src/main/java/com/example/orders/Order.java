package com.example.orders;

import java.time.OffsetDateTime;
import java.util.List;

public record Order(long id, long customerId, String status, OffsetDateTime createdAt, List<Item> items) {

    public record Item(String sku, int qty) {
    }
}
