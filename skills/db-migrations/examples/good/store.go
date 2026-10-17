package main

// selectUser names its columns. With SELECT *, adding a column changes the
// result type of every cached prepared statement that reads users.
const selectUser = `SELECT id, email, display_name, created_at FROM users WHERE id = $1`
