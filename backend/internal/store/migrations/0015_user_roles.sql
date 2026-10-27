-- Roles: an admin does everything, a user only what the routes opened to users allow.
-- New rows are users unless the code says otherwise; every existing user was an admin.
-- A disabled user can't sign in and their sessions and tokens stop working.
ALTER TABLE users ADD COLUMN role TEXT NOT NULL DEFAULT 'user' CHECK (role IN ('admin', 'user'));
ALTER TABLE users ADD COLUMN disabled INTEGER NOT NULL DEFAULT 0 CHECK (disabled IN (0, 1));
UPDATE users SET role = 'admin';
