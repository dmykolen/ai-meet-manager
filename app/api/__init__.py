from app.api import ask, jobs, stream, voices

routers = [jobs.router, voices.router, stream.router, ask.router]
