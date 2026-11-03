### A Pluto.jl notebook ###
# v1.1.0

using Markdown
using InteractiveUtils

# This Pluto notebook uses @bind for interactivity. When running this notebook outside of Pluto, the following 'mock version' of @bind gives bound variables a default value (instead of an error).
macro bind(def, element)
    #! format: off
    return quote
        local iv = try Base.loaded_modules[Base.PkgId(Base.UUID("6e696c72-6542-2067-7265-42206c756150"), "AbstractPlutoDingetjes")].Bonds.initial_value catch; b -> missing; end
        local el = $(esc(element))
        global $(esc(def)) = Core.applicable(Base.get, el) ? Base.get(el) : iv(el)
        el
    end
    #! format: on
end

# ╔═╡ a1579684-4831-45fd-b404-0d4522eda05c
md"""
# Compound interest

Move a slider: every cell that uses its value runs again, in order.
"""

# ╔═╡ 4c8013d6-fa52-4357-a537-ef8ea9d54352
md"""Yearly rate (%): $(@bind rate_slider html"<input type=range min=1 max=15 value=5>")"""

# ╔═╡ 9e279210-9085-42af-ba92-d7aafe59fb0c
md"""Years: $(@bind years_slider html"<input type=range min=1 max=40 value=10>")"""

# ╔═╡ 12419619-0522-4e3f-8328-890de021261a
principal = 1000

# ╔═╡ 3f6c2a0e-8d4b-4c1e-9a57-2b1e6d0c7f41
# A slider's value is missing until the page sends one; start from the slider's own.
rate, years = coalesce(rate_slider, 5), coalesce(years_slider, 10)

# ╔═╡ 24bdd4c5-e379-40a7-9662-a868b43bdd60
balance = [principal * (1 + rate / 100)^y for y in 0:years]

# ╔═╡ 97d0b3ae-41aa-44b3-931b-b5c5b12a927d
md"After **$(years) years** at **$(rate)%**, **$(principal)** grows to **$(round(balance[end]; digits=2))**."

# ╔═╡ 597d2dfb-0016-4da1-a42b-253397aea09e
Text(join([lpad(y, 2) * " " * "█"^round(Int, b / balance[end] * 40) for (y, b) in zip(0:years, balance)], "\n"))

# ╔═╡ Cell order:
# ╟─a1579684-4831-45fd-b404-0d4522eda05c
# ╟─4c8013d6-fa52-4357-a537-ef8ea9d54352
# ╟─9e279210-9085-42af-ba92-d7aafe59fb0c
# ╠═12419619-0522-4e3f-8328-890de021261a
# ╠═3f6c2a0e-8d4b-4c1e-9a57-2b1e6d0c7f41
# ╠═24bdd4c5-e379-40a7-9662-a868b43bdd60
# ╟─97d0b3ae-41aa-44b3-931b-b5c5b12a927d
# ╠═597d2dfb-0016-4da1-a42b-253397aea09e
