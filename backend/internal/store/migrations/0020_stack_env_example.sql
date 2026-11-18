-- A git stack's env example: a file whose variables are only hints for the .env editor,
-- never merged into the stack's .env (unlike env_path). Empty: a .env.example next to
-- the compose file, if there is one.
ALTER TABLE stack_sources ADD COLUMN env_example_path TEXT NOT NULL DEFAULT '';
