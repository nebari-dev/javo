# Summaries and plots of mtcars, a dataset that ships with R.
# Run a line with Ctrl+Enter (Cmd+Enter on a Mac), or the whole file with Ctrl+Shift+Enter.

data(mtcars)
head(mtcars)

# Average fuel economy by number of cylinders.
aggregate(mpg ~ cyl, data = mtcars, FUN = mean)

# Heavier cars use more fuel.
plot(mtcars$wt, mtcars$mpg,
     xlab = "Weight (1000 lb)", ylab = "Miles per gallon",
     main = "Fuel economy and weight", pch = 19)
abline(lm(mpg ~ wt, data = mtcars), col = "steelblue", lwd = 2)

# Fuel economy for each cylinder count.
boxplot(mpg ~ cyl, data = mtcars,
        xlab = "Cylinders", ylab = "Miles per gallon",
        main = "Fuel economy by cylinders")
