-- Create the custom_commands table to store registered custom command scripts.	
CREATE TABLE IF NOT EXISTS custom_commands (
	name TEXT PRIMARY KEY,
	description TEXT,
	script_path TEXT NOT NULL,
	node_id TEXT NOT NULL
);