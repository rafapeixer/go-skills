package com.example.orders;

import java.sql.PreparedStatement;
import java.time.OffsetDateTime;
import java.util.ArrayList;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;
import java.util.Objects;
import java.util.Optional;
import org.springframework.jdbc.core.JdbcTemplate;
import org.springframework.jdbc.core.RowCallbackHandler;
import org.springframework.stereotype.Repository;
import org.springframework.transaction.PlatformTransactionManager;
import org.springframework.transaction.support.TransactionTemplate;

@Repository
public class OrderRepository {

    private static final String INSERT_ORDER =
            "INSERT INTO orders (customer_id) VALUES (?) RETURNING id";

    private static final String INSERT_ITEMS = """
            INSERT INTO order_items (order_id, sku, qty)
            SELECT ?, t.sku, t.qty FROM unnest(?::text[], ?::int[]) AS t(sku, qty)
            """;

    private static final String FIND_BY_ID = """
            SELECT o.id, o.customer_id, o.status, o.created_at, i.sku, i.qty
            FROM orders o
            LEFT JOIN order_items i ON i.order_id = o.id
            WHERE o.id = ?
            ORDER BY i.sku
            """;

    // Keyset pagination over orders_customer_created (customer_id, created_at DESC, id DESC),
    // with items joined in the same round trip.
    private static final String PAGE_FIRST = """
            WITH page AS (
                SELECT id, customer_id, status, created_at FROM orders
                WHERE customer_id = ?
                ORDER BY created_at DESC, id DESC
                LIMIT ?
            )
            SELECT p.id, p.customer_id, p.status, p.created_at, i.sku, i.qty
            FROM page p
            LEFT JOIN order_items i ON i.order_id = p.id
            ORDER BY p.created_at DESC, p.id DESC, i.sku
            """;

    private static final String PAGE_AFTER = """
            WITH page AS (
                SELECT id, customer_id, status, created_at FROM orders
                WHERE customer_id = ? AND (created_at, id) < (?, ?)
                ORDER BY created_at DESC, id DESC
                LIMIT ?
            )
            SELECT p.id, p.customer_id, p.status, p.created_at, i.sku, i.qty
            FROM page p
            LEFT JOIN order_items i ON i.order_id = p.id
            ORDER BY p.created_at DESC, p.id DESC, i.sku
            """;

    private final JdbcTemplate jdbc;
    private final TransactionTemplate tx;

    public OrderRepository(JdbcTemplate jdbc, PlatformTransactionManager txManager) {
        this.jdbc = jdbc;
        this.tx = new TransactionTemplate(txManager);
    }

    /** Inserts the order and all items in one transaction (two round trips). */
    public long create(long customerId, List<Order.Item> items) {
        String[] skus = items.stream().map(Order.Item::sku).toArray(String[]::new);
        Integer[] qtys = items.stream().map(Order.Item::qty).toArray(Integer[]::new);
        Long id = tx.execute(status -> {
            Long orderId = Objects.requireNonNull(jdbc.queryForObject(INSERT_ORDER, Long.class, customerId));
            jdbc.update(con -> {
                PreparedStatement ps = con.prepareStatement(INSERT_ITEMS);
                ps.setLong(1, orderId);
                ps.setArray(2, con.createArrayOf("text", skus));
                ps.setArray(3, con.createArrayOf("int4", qtys));
                return ps;
            });
            return orderId;
        });
        return Objects.requireNonNull(id);
    }

    public Optional<Order> find(long id) {
        return queryOrders(FIND_BY_ID, id).stream().findFirst();
    }

    /** Returns up to {@code limit} orders, newest first, strictly after {@code after} if given. */
    public List<Order> findByCustomer(long customerId, Cursor after, int limit) {
        if (after == null) {
            return queryOrders(PAGE_FIRST, customerId, limit);
        }
        return queryOrders(PAGE_AFTER, customerId, after.createdAt(), after.id(), limit);
    }

    /** Folds (order, item) join rows into orders, preserving row order. */
    private List<Order> queryOrders(String sql, Object... args) {
        Map<Long, OrderRow> rows = new LinkedHashMap<>();
        jdbc.query(sql, (RowCallbackHandler) rs -> {
            long id = rs.getLong("id");
            OrderRow row = rows.get(id);
            if (row == null) {
                row = new OrderRow(id, rs.getLong("customer_id"), rs.getString("status"),
                        rs.getObject("created_at", OffsetDateTime.class), new ArrayList<>());
                rows.put(id, row);
            }
            String sku = rs.getString("sku");
            if (sku != null) {
                row.items().add(new Order.Item(sku, rs.getInt("qty")));
            }
        }, args);
        return rows.values().stream()
                .map(r -> new Order(r.id(), r.customerId(), r.status(), r.createdAt(), List.copyOf(r.items())))
                .toList();
    }

    private record OrderRow(long id, long customerId, String status, OffsetDateTime createdAt, List<Order.Item> items) {
    }
}
